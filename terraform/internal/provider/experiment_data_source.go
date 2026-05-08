// experiment_data_source.go — read-only data source for litemlflow_experiment.
// Looks up an experiment by name: GET /api/2.0/mlflow/experiments/get-by-name
package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSource = &experimentDataSource{}

func newExperimentDataSource() datasource.DataSource { return &experimentDataSource{} }

type experimentDataSource struct{ client *Client }

type experimentDataSourceModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	ArtifactLocation types.String `tfsdk:"artifact_location"`
	Tags             types.Map    `tfsdk:"tags"`
}

func (d *experimentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_experiment"
}

func (d *experimentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads an existing LiteMLflow experiment by name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Experiment ID assigned by the server.",
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Experiment name to look up.",
			},
			"artifact_location": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Artifact storage URI for this experiment.",
			},
			"tags": schema.MapAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Tags attached to this experiment.",
			},
		},
	}
}

func (d *experimentDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *experimentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config experimentDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	info, err := d.client.GetExperimentByName(config.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read experiment data source failed", err.Error())
		return
	}

	tagMap := make(map[string]attr.Value, len(info.Tags))
	for _, t := range info.Tags {
		tagMap[t.Key] = types.StringValue(t.Value)
	}
	tags, _ := types.MapValue(types.StringType, tagMap)

	state := experimentDataSourceModel{
		ID:               types.StringValue(info.ExperimentID),
		Name:             types.StringValue(info.Name),
		ArtifactLocation: types.StringValue(info.ArtifactLocation),
		Tags:             tags,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}
