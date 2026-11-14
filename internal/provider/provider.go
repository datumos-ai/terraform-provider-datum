package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const (
	envBaseURL = "DATUM_BASE_URL"
	envAPIKey  = "DATUM_API_KEY"
)

var _ provider.Provider = &datumProvider{}

type datumProvider struct {
	version string
}

type datumProviderModel struct {
	Host   types.String `tfsdk:"host"`
	APIKey types.String `tfsdk:"api_key"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &datumProvider{version: version}
	}
}

func (p *datumProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "datum"
	resp.Version = p.version
}

func (p *datumProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Provisions Datum spaces and their configuration through the server-side space manifest. Two modes: " +
			"ORG-ADMIN — `host` is the organisation host, `api_key` an org-admin key, `datum_space` and `datum_space_manifest.space_id` name the space; " +
			"SPACE-ADMIN — `host` is the space's own host (https://<slug>.<domain>), `api_key` a key bound to that space, and `datum_space_manifest` omits `space_id` (only space-tier kinds).",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Optional:    true,
				Description: "The Datum host this configuration talks to: the organisation host (`https://datum.example.com`) in org-admin mode, the space's host (`https://<slug>.datum.example.tech`) in space-admin mode. Falls back to the `" + envBaseURL + "` environment variable — check it, a shell that exports it for the datum CLI sends the plan there.",
			},
			"api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "API key: an org-admin key minted on the organisation host (org-admin mode), or a key bound to the space, minted on the space's host (space-admin mode). Falls back to the `" + envAPIKey + "` environment variable. Never commit it — pass it from the environment or a secret store.",
			},
		},
	}
}

func (p *datumProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg datumProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	host := os.Getenv(envBaseURL)
	if !cfg.Host.IsNull() && cfg.Host.ValueString() != "" {
		host = cfg.Host.ValueString()
	}
	apiKey := os.Getenv(envAPIKey)
	if !cfg.APIKey.IsNull() && cfg.APIKey.ValueString() != "" {
		apiKey = cfg.APIKey.ValueString()
	}
	if host == "" {
		resp.Diagnostics.AddAttributeError(path.Root("host"), "Missing Datum host",
			"Set the provider's `host` attribute or the "+envBaseURL+" environment variable.")
	}
	if apiKey == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing Datum API key",
			"Set the provider's `api_key` attribute or the "+envAPIKey+" environment variable.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	client := NewClient(host, apiKey, "terraform-provider-datum/"+p.version)
	// Say which instance this run talks to — a shell that already exports
	// DATUM_BASE_URL for the datum CLI silently sends a plan elsewhere.
	tflog.Info(ctx, "datum provider configured", map[string]any{"host": client.BaseURL()})
	resp.Diagnostics.AddWarning("Datum host", "This configuration talks to "+client.BaseURL()+
		" (from the provider's `host` attribute or "+envBaseURL+"). Check it before applying.")
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *datumProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewSpaceResource,
		NewSpaceManifestResource,
	}
}

func (p *datumProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
