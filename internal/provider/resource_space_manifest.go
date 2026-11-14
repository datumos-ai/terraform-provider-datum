package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &spaceManifestResource{}
	_ resource.ResourceWithConfigure   = &spaceManifestResource{}
	_ resource.ResourceWithImportState = &spaceManifestResource{}
	_ resource.ResourceWithModifyPlan  = &spaceManifestResource{}
)

type spaceManifestResource struct {
	client *Client
}

type spaceManifestModel struct {
	ID      types.String         `tfsdk:"id"`
	SpaceID types.String         `tfsdk:"space_id"`
	Spec    jsontypes.Normalized `tfsdk:"spec"`
	Prune   types.Bool           `tfsdk:"prune"`
}

func NewSpaceManifestResource() resource.Resource { return &spaceManifestResource{} }

func (r *spaceManifestResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_space_manifest"
}

func (r *spaceManifestResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The desired configuration of one Datum space as a declarative manifest (`apiVersion: datum/v1`). " +
			"The provider sends the spec to the server's manifest engine, which validates it, plans the difference against the live space " +
			"and applies it. Only the KINDS present in `spec` are managed; within a list kind only the entries you name are managed unless `prune` is true. " +
			"The schema of `spec` is served by the Datum instance at `GET /api/manifest/schema` — it grows with the platform, this provider does not need updating.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Same as `space_id` — one manifest per space.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"space_id": schema.StringAttribute{
				Optional: true,
				Description: "Id of the space this manifest configures (`datum_space.x.id`) — the ORG-ADMIN mode, on the organisation host. " +
					"Omit it for the SPACE-ADMIN mode: the provider then points at the space's own host (`host = https://<slug>.<domain>`) with a space-bound key, " +
					"the server resolves the space from the host, and only space-tier kinds may appear in `spec`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"spec": schema.StringAttribute{
				Required:    true,
				CustomType:  jsontypes.NormalizedType{},
				Description: "The manifest as a JSON document (use `jsonencode(...)`). Top-level keys are KINDS; the authoritative list — with every field, closed vocabulary and the org/space tier — is the instance's `GET /api/manifest/schema`, which grows with the platform. Secrets never travel here: credentials are referenced by label.",
			},
			"prune": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "When true, entries of a list kind (members, browser profiles…) that exist in the space but are absent from `spec` are DELETED. Default false: unlisted entries are left alone and reported as `orphan` in the plan.",
			},
		},
	}
}

func (r *spaceManifestResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}

// ModifyPlan runs a server-side dry run so validation errors (bad kind, an
// unresolved credential reference, an org-tier setting) surface in
// `terraform plan`, not at apply, and the planned operations are visible.
func (r *spaceManifestResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || r.client == nil {
		return
	}
	var plan spaceManifestModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Spec.IsUnknown() || plan.Prune.IsUnknown() {
		return
	}
	if plan.SpaceID.IsUnknown() {
		// The space is created in this same run: no live state to dry-run
		// against, but the document's SHAPE can still be checked — a bad kind
		// must fail the plan, not leave a half-configured space after apply.
		// (A NULL space_id is the space-admin mode — the dry run below runs
		// against the request space.)
		if err := r.client.ValidateManifest(ctx, json.RawMessage(plan.Spec.ValueString())); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("spec"), "Datum rejected the manifest", err.Error())
		}
		return
	}
	res, err := r.client.ApplyManifest(ctx, plan.SpaceID.ValueString(), json.RawMessage(plan.Spec.ValueString()), true, plan.Prune.ValueBool())
	if err != nil {
		if IsNotFound(err) {
			// A known space_id the server does not know: a typo, or a space
			// that is gone — apply would fail the same way, so fail the plan.
			resp.Diagnostics.AddAttributeError(path.Root("space_id"), "Datum knows no such space",
				"The dry run answered 404 for space_id "+plan.SpaceID.ValueString()+": "+err.Error())
			return
		}
		resp.Diagnostics.AddAttributeError(path.Root("spec"), "Datum rejected the manifest", err.Error())
		return
	}
	for _, w := range res.Warnings {
		resp.Diagnostics.AddAttributeWarning(path.Root("spec"), "Manifest warning", w)
	}
	if orphans := orphansOf(res.Plan); len(orphans) > 0 {
		resp.Diagnostics.AddAttributeWarning(path.Root("spec"), "Entries in the space this manifest does not manage",
			"The space holds entries the spec does not name; they are left alone (prune = false). "+
				"Add them to the spec to manage them, or set prune = true to delete them:\n  "+strings.Join(orphans, "\n  "))
	}
	if summary := summarizePlan(res.Plan); summary != "" {
		tflog.Info(ctx, "datum manifest plan", map[string]any{"space": res.SpaceID, "ops": summary})
		// Terraform shows only the HCL diff of `spec`; the server's per-entry
		// plan is what tells apart "adopting existing rows" from "overwriting
		// them" — surface it.
		resp.Diagnostics.AddAttributeWarning(path.Root("spec"), "Datum server plan (space "+res.SpaceID+")",
			"Entries the apply would change:\n  "+strings.ReplaceAll(summary, ", ", "\n  "))
	}
}

func orphansOf(ops []ManifestOp) []string {
	out := []string{}
	for _, op := range ops {
		if op.Op == "orphan" {
			out = append(out, op.Kind+"/"+op.Name)
		}
	}
	return out
}

func (r *spaceManifestResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan spaceManifestModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.apply(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *spaceManifestResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state spaceManifestModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A dry run of the RECORDED spec answers the live state projected onto
	// what this manifest manages; a non-empty plan means drift, and the
	// projected observation becomes the new state so `terraform plan` shows
	// the difference.
	res, err := r.client.ApplyManifest(ctx, state.SpaceID.ValueString(), json.RawMessage(state.Spec.ValueString()), true, state.Prune.ValueBool())
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		if IsManifestInvalid(err) {
			// The RECORDED spec no longer validates on this server — its
			// schema moved on (a field dropped, a value narrowed) after the
			// last apply. Failing here would block every plan, the fix
			// included: Read never sees the configuration. Keep the state;
			// the plan compares it with the configuration, whose own dry run
			// (ModifyPlan) still refuses a spec that is wrong now, and the
			// apply replaces the recorded one.
			resp.Diagnostics.AddWarning("The recorded space manifest no longer validates",
				err.Error()+"\n\nDrift could not be read. The state keeps the spec of the last apply; apply the current configuration (with the refused fields fixed) to replace it.")
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
		resp.Diagnostics.AddError("Reading space manifest failed", err.Error())
		return
	}
	if hasChanges(res.Plan) {
		tflog.Info(ctx, "datum manifest drift", map[string]any{"space": state.SpaceID.ValueString(), "ops": summarizePlan(res.Plan)})
		state.Spec = jsontypes.NewNormalizedValue(string(res.Observed))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *spaceManifestResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan spaceManifestModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.apply(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete forgets the manifest. It does NOT undo the configuration it applied:
// removing management of a space must never delete its members or settings.
// Delete the `datum_space` resource to remove the space itself.
func (r *spaceManifestResource) Delete(ctx context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	tflog.Info(ctx, "datum_space_manifest removed from state; the space's configuration is left as is")
}

func (r *spaceManifestResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// `terraform import datum_space_manifest.x <spaceId>` — the spec starts as
	// the empty manifest (manages nothing); the next plan against the user's
	// spec shows what apply would change.
	// `terraform import datum_space_manifest.x <spaceId>` (org-admin mode) or
	// `... self` (space-admin mode — the host names the space, and the id
	// stored is the space's, never the import token: apply would otherwise
	// change `id` and Terraform refuses an inconsistent result).
	id := req.ID
	if req.ID == "self" {
		if r.client == nil {
			resp.Diagnostics.AddError("Provider not configured", "cannot resolve `self` without a configured provider")
			return
		}
		resolved, err := r.client.SpaceManifestID(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Resolving the space failed", err.Error())
			return
		}
		id = resolved
	} else {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("space_id"), req.ID)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("spec"), jsontypes.NewNormalizedValue(`{"apiVersion":"datum/v1"}`))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("prune"), false)...)
}

func (r *spaceManifestResource) apply(ctx context.Context, plan *spaceManifestModel, diags *diag.Diagnostics) {
	res, err := r.client.ApplyManifest(ctx, plan.SpaceID.ValueString(), json.RawMessage(plan.Spec.ValueString()), false, plan.Prune.ValueBool())
	if err != nil {
		diags.AddAttributeError(path.Root("spec"), "Applying the space manifest failed", err.Error())
		return
	}
	for _, w := range res.Warnings {
		diags.AddAttributeWarning(path.Root("spec"), "Manifest warning", w)
	}
	tflog.Info(ctx, "datum manifest applied", map[string]any{"space": plan.SpaceID.ValueString(), "ops": summarizePlan(res.Plan)})
	if res.SpaceID != "" {
		plan.ID = types.StringValue(res.SpaceID)
	} else {
		plan.ID = plan.SpaceID
	}
	// `spec` is a Required attribute: the state written after apply MUST
	// equal the planned (config) value or Terraform fails the apply as an
	// inconsistent result. So the spec stays the user's; when the server's
	// projected observation differs (a value it normalised), say so — the
	// next plan will show the difference, and the fix is to write the
	// canonical form the warning quotes.
	if len(res.Observed) > 0 && !jsonEqual(res.Observed, json.RawMessage(plan.Spec.ValueString())) {
		diags.AddAttributeWarning(path.Root("spec"), "Datum stored a normalised form of the manifest",
			"The server's observation differs from the spec after apply — the next plan will show it as drift. "+
				"Write the canonical form Datum reports:\n"+string(res.Observed))
	}
}

func hasChanges(ops []ManifestOp) bool {
	for _, op := range ops {
		if op.Op != "noop" && op.Op != "orphan" {
			return true
		}
	}
	return false
}

func summarizePlan(ops []ManifestOp) string {
	parts := make([]string, 0, len(ops))
	for _, op := range ops {
		if op.Op == "noop" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s/%s", op.Op, op.Kind, op.Name))
	}
	return strings.Join(parts, ", ")
}

func jsonEqual(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(ab) == string(bb)
}
