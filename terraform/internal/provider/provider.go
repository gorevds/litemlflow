// provider.go — LiteMLflow provider type, schema, and configuration.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure litemlflowProvider satisfies the provider.Provider interface.
var _ provider.Provider = &litemlflowProvider{}
var _ provider.ProviderWithFunctions = &litemlflowProvider{}

// litemlflowProvider is the top-level provider implementation.
type litemlflowProvider struct {
	version string
}

// New returns a function that constructs the provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &litemlflowProvider{version: version}
	}
}

// providerModel mirrors the HCL provider block attributes.
type providerModel struct {
	URL      types.String `tfsdk:"url"`
	Auth     types.String `tfsdk:"auth"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
}

func (p *litemlflowProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "litemlflow"
	resp.Version = p.version
}

func (p *litemlflowProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Provider for managing LiteMLflow experiments, prompts, and registered models via Terraform.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				MarkdownDescription: "Base URL of the LiteMLflow server (e.g. `https://lmf.example.com`). Defaults to the `LITEMLFLOW_URL` environment variable.",
				Optional:            true,
			},
			"auth": schema.StringAttribute{
				MarkdownDescription: "Authentication mode. Currently only `\"basic\"` is supported. Defaults to the `LITEMLFLOW_AUTH` environment variable.",
				Optional:            true,
			},
			"username": schema.StringAttribute{
				MarkdownDescription: "Username for basic authentication. Defaults to the `LITEMLFLOW_BASIC_USER` environment variable.",
				Optional:            true,
			},
			"password": schema.StringAttribute{
				MarkdownDescription: "Password for basic authentication (plain text; the provider sends it over TLS). Defaults to the `LITEMLFLOW_BASIC_PASS` environment variable.",
				Optional:            true,
				Sensitive:           true,
			},
		},
	}
}

func (p *litemlflowProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Resolve each setting: explicit config → environment variable → default.
	url := resolveString(config.URL, "LITEMLFLOW_URL", "")
	if url == "" {
		resp.Diagnostics.AddError(
			"Missing provider URL",
			"The LiteMLflow provider requires a server URL. Set the `url` attribute or the LITEMLFLOW_URL environment variable.",
		)
		return
	}

	username := resolveString(config.Username, "LITEMLFLOW_BASIC_USER", "")
	password := resolveString(config.Password, "LITEMLFLOW_BASIC_PASS", "")

	client := newClient(url, username, password)
	resp.DataSourceData = client
	resp.ResourceData = client
}

func (p *litemlflowProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newExperimentResource,
		newPromptResource,
		newPromptAliasResource,
		newRegisteredModelResource,
		newWorkspaceResource,
	}
}

func (p *litemlflowProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newExperimentDataSource,
		newPromptDataSource,
	}
}

func (p *litemlflowProvider) Functions(_ context.Context) []func() function.Function {
	return nil
}

// resolveString returns the first non-empty value among: tfValue (if known and not null),
// os.Getenv(envKey), then fallback.
func resolveString(tfValue types.String, envKey, fallback string) string {
	if !tfValue.IsNull() && !tfValue.IsUnknown() && tfValue.ValueString() != "" {
		return tfValue.ValueString()
	}
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return fallback
}
