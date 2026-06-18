// prompt_data_source.go — read-only data source for litemlflow_prompt.
// Can read the latest version or a specific version:
//
//	GET /api/v1/prompts/{name}            (version = 0 or unset → latest)
//	GET /api/v1/prompts/{name}/versions/N  (specific version)
package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &promptDataSource{}

func newPromptDataSource() datasource.DataSource { return &promptDataSource{} }

type promptDataSource struct{ client *Client }

type promptDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Version     types.Int64  `tfsdk:"version"`
	Content     types.String `tfsdk:"content"`
	Description types.String `tfsdk:"description"`
	ContentHash types.String `tfsdk:"content_hash"`
}

func (d *promptDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_prompt"
}

func (d *promptDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads a LiteMLflow prompt. Set `version` to read a specific version; omit it (or set to `0`) to read the latest.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Composite ID `<name>@<version>`.",
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Prompt name to read.",
			},
			"version": schema.Int64Attribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Specific version to read. Omit or set to `0` for the latest version.",
			},
			"content": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Prompt text content.",
			},
			"description": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Prompt description.",
			},
			"content_hash": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SHA-256 content hash.",
			},
		},
	}
}

func (d *promptDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *Client, got %T", req.ProviderData))
		return
	}
	d.client = client
}

func (d *promptDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config promptDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var info *promptInfo
	var err error

	if config.Version.IsNull() || config.Version.IsUnknown() || config.Version.ValueInt64() == 0 {
		info, err = d.client.GetPromptLatest(config.Name.ValueString())
	} else {
		info, err = d.client.GetPromptVersion(config.Name.ValueString(), int(config.Version.ValueInt64()))
	}
	if err != nil {
		resp.Diagnostics.AddError("Read prompt data source failed", err.Error())
		return
	}

	state := promptDataSourceModel{
		ID:          types.StringValue(fmt.Sprintf("%s@%d", info.Name, info.Version)),
		Name:        types.StringValue(info.Name),
		Version:     types.Int64Value(int64(info.Version)),
		Content:     types.StringValue(info.Content),
		Description: types.StringValue(info.Description),
		ContentHash: types.StringValue(info.ContentHash),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}
