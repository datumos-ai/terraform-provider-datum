package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	spaceStatusActive    = "active"
	spaceStatusSuspended = "suspended"
)

var (
	_ resource.Resource                = &spaceResource{}
	_ resource.ResourceWithConfigure   = &spaceResource{}
	_ resource.ResourceWithImportState = &spaceResource{}
)

type spaceResource struct {
	client *Client
}

type spaceModel struct {
	ID                    types.String `tfsdk:"id"`
	Slug                  types.String `tfsdk:"slug"`
	DisplayName           types.String `tfsdk:"display_name"`
	Status                types.String `tfsdk:"status"`
	NamespaceCapabilities types.Bool   `tfsdk:"namespace_capabilities"`
	NamespaceMembership   types.Bool   `tfsdk:"namespace_membership"`
	IncidentPublishers    types.List   `tfsdk:"incident_publishers"`
}

func NewSpaceResource() resource.Resource { return &spaceResource{} }

func (r *spaceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_space"
}

func (r *spaceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Datum space (tenant). This resource owns the space's existence and identity; its CONTENTS — features, members, settings, browser profiles — are managed by `datum_space_manifest`. " +
			"Datum never deletes a space (its rows carry the team's history): destroying this resource SUSPENDS the space, and a later `terraform import` adopts it back.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Server-assigned space id — pass it to `datum_space_manifest.space_id`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"slug": schema.StringAttribute{
				Required: true,
				Description: "The subdomain label (`<slug>.<spaces domain>`), also the webhook identity. Changing it renames the space IN PLACE: " +
					"the previous slug stays a permanent alias of the space (browsers get a 301, webhooks, satellites and API clients keep working on the old host) " +
					"and is reserved against every other space.",
			},
			"display_name": schema.StringAttribute{
				Required:    true,
				Description: "Human name shown in the panel (1–80 characters).",
			},
			"status": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(spaceStatusActive),
				Description: "`active` (default) or `suspended`. A suspended space keeps its data but answers nothing.",
				Validators:  []validator.String{oneOf(spaceStatusActive, spaceStatusSuspended)},
			},
			// No default: unset = whatever the server holds — a new
			// space starts with capabilities ON, and a provider upgrade never
			// re-sends a value nobody wrote in the configuration.
			"namespace_capabilities": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
				Description: "Namespace capabilities : ON mounts what the space's Develop grants reach — repository claims and tokens, and read-only AWS roles for every run of the space. Repositories are cut to the AD-group coverage of the person behind each run (a routine's run: its owner's). " +
					"OFF cuts them entirely. A new space starts ON; unset keeps the server's value.",
			},
			"namespace_membership": schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
				Description: "Namespace membership : ON derives membership from the AD groups — everyone covered by the space's required namespaces acts as an operator WITHOUT a membership row, and the space's Entra group bindings are dormant. " +
					"An org-admin decision with access-model consequences; read the coverage report (`GET /api/spaces/{id}/namespace-gating`) first. A new space starts OFF; unset keeps the server's value.",
			},
			"incident_publishers": schema.ListAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
				Default:     listdefault.StaticValue(types.ListValueMust(types.StringType, []attr.Value{})),
				Description: "Nicks of the members who may create/publish incident comms through this space. Non-empty = everyone else is a status-pages viewer whatever their space role; empty = no restriction.",
			},
		},
	}
}

func (r *spaceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *spaceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan spaceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	space, err := r.client.CreateSpace(ctx, plan.Slug.ValueString(), plan.DisplayName.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Creating space failed", err.Error())
		return
	}
	if patch := spacePatch(ctx, &plan, spaceToModel(space), &resp.Diagnostics); len(patch) > 0 {
		if space, err = r.client.PatchSpace(ctx, space.ID, patch); err != nil {
			resp.Diagnostics.AddError("Configuring space failed", err.Error())
			return
		}
	}
	if plan.Status.ValueString() != space.Status {
		if space, err = r.client.SetSpaceStatus(ctx, space.ID, plan.Status.ValueString()); err != nil {
			resp.Diagnostics.AddError("Setting the space status failed", err.Error())
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, spaceToModel(space))...)
}

func (r *spaceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state spaceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	space, err := r.client.GetSpace(ctx, state.ID.ValueString())
	if err != nil {
		if IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Reading space failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, spaceToModel(space))...)
}

func (r *spaceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state spaceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	space, err := r.client.GetSpace(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Reading space failed", err.Error())
		return
	}
	if patch := spacePatch(ctx, &plan, state, &resp.Diagnostics); len(patch) > 0 {
		if space, err = r.client.PatchSpace(ctx, id, patch); err != nil {
			resp.Diagnostics.AddError("Updating space failed", err.Error())
			return
		}
	}
	if plan.Status.ValueString() != state.Status.ValueString() {
		if space, err = r.client.SetSpaceStatus(ctx, id, plan.Status.ValueString()); err != nil {
			resp.Diagnostics.AddError("Setting the space status failed", err.Error())
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, spaceToModel(space))...)
}

// Delete suspends the space. Datum keeps a space's rows for good — there is
// no DELETE on the API — so "destroy" means "stop serving it".
func (r *spaceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state spaceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Status.ValueString() == spaceStatusSuspended {
		return
	}
	if _, err := r.client.SetSpaceStatus(ctx, state.ID.ValueString(), spaceStatusSuspended); err != nil && !IsNotFound(err) {
		resp.Diagnostics.AddError("Suspending space failed", err.Error())
	}
}

func (r *spaceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func spaceToModel(s *Space) spaceModel {
	publishers := make([]attr.Value, 0, len(s.IncidentPublishers))
	for _, p := range s.IncidentPublishers {
		publishers = append(publishers, types.StringValue(p))
	}
	return spaceModel{
		ID:                    types.StringValue(s.ID),
		Slug:                  types.StringValue(s.Slug),
		DisplayName:           types.StringValue(s.DisplayName),
		Status:                types.StringValue(s.Status),
		NamespaceCapabilities: types.BoolValue(s.NamespaceCapabilities),
		NamespaceMembership:   types.BoolValue(s.NamespaceMembership),
		IncidentPublishers:    types.ListValueMust(types.StringType, publishers),
	}
}

// spacePatch is the PATCH body that moves `from` to `plan` — only the
// attributes that differ, so an untouched field is never re-sent.
func spacePatch(ctx context.Context, plan *spaceModel, from spaceModel, diags *diag.Diagnostics) map[string]any {
	patch := map[string]any{}
	if plan.Slug.ValueString() != from.Slug.ValueString() {
		patch["slug"] = plan.Slug.ValueString()
	}
	if plan.DisplayName.ValueString() != from.DisplayName.ValueString() {
		patch["displayName"] = plan.DisplayName.ValueString()
	}
	if !plan.NamespaceCapabilities.IsUnknown() && plan.NamespaceCapabilities.ValueBool() != from.NamespaceCapabilities.ValueBool() {
		patch["namespaceCapabilities"] = plan.NamespaceCapabilities.ValueBool()
	}
	if !plan.NamespaceMembership.IsUnknown() && plan.NamespaceMembership.ValueBool() != from.NamespaceMembership.ValueBool() {
		patch["namespaceMembership"] = plan.NamespaceMembership.ValueBool()
	}
	if !plan.IncidentPublishers.IsUnknown() && !plan.IncidentPublishers.Equal(from.IncidentPublishers) {
		var publishers []string
		diags.Append(plan.IncidentPublishers.ElementsAs(ctx, &publishers, false)...)
		if publishers == nil {
			publishers = []string{}
		}
		patch["incidentPublishers"] = publishers
	}
	return patch
}

// oneOf is a tiny enum validator (the framework-validators module would be a
// whole dependency for one attribute).
type oneOfValidator struct{ allowed []string }

func oneOf(values ...string) validator.String { return oneOfValidator{allowed: values} }

func (v oneOfValidator) Description(_ context.Context) string {
	return fmt.Sprintf("must be one of %v", v.allowed)
}
func (v oneOfValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v oneOfValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, a := range v.allowed {
		if req.ConfigValue.ValueString() == a {
			return
		}
	}
	resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", v.Description(context.Background()))
}
