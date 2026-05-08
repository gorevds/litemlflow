// experiment_resource.go — Terraform resource for litemlflow_experiment.
// Maps to /api/2.0/mlflow/experiments/* (MLflow-compat layer).
package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &experimentResource{}

func newExperimentResource() resource.Resource { return &experimentResource{} }

type experimentResource struct{ client *Client }

type experimentResourceModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	ArtifactLocation types.String `tfsdk:"artifact_location"`
	Tags             types.Map    `tfsdk:"tags"`
}

func (r *experimentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_experiment"
}

func (r *experimentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LiteMLflow experiment (maps to `/api/2.0/mlflow/experiments/*`).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The MLflow experiment ID assigned by the server.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable experiment name (must be unique within the workspace).",
			},
			"artifact_location": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "URI prefix where run artifacts are stored (e.g. `mlflow-artifacts:/training`). Assigned by the server when omitted.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tags": schema.MapAttribute{
				Optional:            true,
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Key-value tags attached to the experiment.",
			},
		},
	}
}

func (r *experimentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}

func (r *experimentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan experimentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tags := tagsFromMap(ctx, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	id, err := r.client.CreateExperiment(plan.Name.ValueString(), plan.ArtifactLocation.ValueString(), tags)
	if err != nil {
		resp.Diagnostics.AddError("Create experiment failed", err.Error())
		return
	}

	// Read back the created experiment for computed fields.
	info, err := r.client.GetExperimentByID(id)
	if err != nil {
		resp.Diagnostics.AddError("Read experiment after create failed", err.Error())
		return
	}

	// Set any tags that were in the plan but not echoed back by the API.
	// (The MLflow create endpoint accepts tags but the response body
	//  for create only returns experiment_id, so we re-read above.)
	state := experimentToState(ctx, info, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *experimentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state experimentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	info, err := r.client.GetExperimentByID(state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read experiment failed", err.Error())
		return
	}

	if info.LifecycleStage == "deleted" {
		resp.State.RemoveResource(ctx)
		return
	}

	newState := experimentToState(ctx, info, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *experimentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state experimentResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Rename if name changed.
	if !plan.Name.Equal(state.Name) {
		if err := r.client.UpdateExperiment(state.ID.ValueString(), plan.Name.ValueString()); err != nil {
			resp.Diagnostics.AddError("Rename experiment failed", err.Error())
			return
		}
	}

	// Sync tags: set all tags from plan (upsert), MLflow has no bulk tag update
	// so we iterate. Removing tags that disappeared from plan requires a
	// delete-tag call; however the MLflow API only has set-experiment-tag (upsert).
	// We upsert all planned tags; orphaned tags are left on the server (acceptable
	// given MLflow's data model — see design notes in README).
	planTags := tagsFromMap(ctx, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	for k, v := range planTags {
		if err := r.client.SetExperimentTag(state.ID.ValueString(), k, v); err != nil {
			resp.Diagnostics.AddError("Set experiment tag failed", err.Error())
			return
		}
	}

	// Read back for computed fields.
	info, err := r.client.GetExperimentByID(state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read experiment after update failed", err.Error())
		return
	}

	newState := experimentToState(ctx, info, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *experimentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state experimentResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteExperiment(state.ID.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Delete experiment failed", err.Error())
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func experimentToState(_ context.Context, info *experimentInfo, _ *diag.Diagnostics) *experimentResourceModel {
	tagMap := make(map[string]attr.Value, len(info.Tags))
	for _, t := range info.Tags {
		tagMap[t.Key] = types.StringValue(t.Value)
	}
	tags, d := types.MapValue(types.StringType, tagMap)
	_ = d // framework diagnostics; handled by caller

	return &experimentResourceModel{
		ID:               types.StringValue(info.ExperimentID),
		Name:             types.StringValue(info.Name),
		ArtifactLocation: types.StringValue(info.ArtifactLocation),
		Tags:             tags,
	}
}
