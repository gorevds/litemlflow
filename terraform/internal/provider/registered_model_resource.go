// registered_model_resource.go — Terraform resource for litemlflow_registered_model.
// Maps to /api/2.0/mlflow/registered-models/*.
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

var _ resource.Resource = &registeredModelResource{}

func newRegisteredModelResource() resource.Resource { return &registeredModelResource{} }

type registeredModelResource struct{ client *Client }

type registeredModelResourceModel struct {
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Tags        types.Map    `tfsdk:"tags"`
}

func (r *registeredModelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_registered_model"
}

func (r *registeredModelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a registered model in LiteMLflow's model registry (maps to `/api/2.0/mlflow/registered-models/*`).",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Model name (primary key, immutable — renaming requires recreate).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Free-text description of the model.",
			},
			"tags": schema.MapAttribute{
				Optional:            true,
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Key-value tags attached to the registered model.",
			},
		},
	}
}

func (r *registeredModelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *registeredModelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan registeredModelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tags := tagsFromMap(ctx, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.CreateRegisteredModel(plan.Name.ValueString(), plan.Description.ValueString(), tags); err != nil {
		resp.Diagnostics.AddError("Create registered model failed", err.Error())
		return
	}

	info, err := r.client.GetRegisteredModel(plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read registered model after create failed", err.Error())
		return
	}

	state := registeredModelToState(ctx, info, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *registeredModelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state registeredModelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	info, err := r.client.GetRegisteredModel(state.Name.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read registered model failed", err.Error())
		return
	}

	newState := registeredModelToState(ctx, info, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *registeredModelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state registeredModelResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Update description.
	if !plan.Description.Equal(state.Description) {
		if err := r.client.UpdateRegisteredModel(state.Name.ValueString(), plan.Description.ValueString()); err != nil {
			resp.Diagnostics.AddError("Update registered model description failed", err.Error())
			return
		}
	}

	// Sync tags (upsert all planned tags).
	planTags := tagsFromMap(ctx, plan.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	for k, v := range planTags {
		if err := r.client.SetRegisteredModelTag(state.Name.ValueString(), k, v); err != nil {
			resp.Diagnostics.AddError("Set registered model tag failed", err.Error())
			return
		}
	}

	// Delete tags that are no longer in the plan.
	stateTags := tagsFromMap(ctx, state.Tags, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	for k := range stateTags {
		if _, exists := planTags[k]; !exists {
			if err := r.client.DeleteRegisteredModelTag(state.Name.ValueString(), k); err != nil && !isNotFound(err) {
				resp.Diagnostics.AddError("Delete registered model tag failed", err.Error())
				return
			}
		}
	}

	info, err := r.client.GetRegisteredModel(state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read registered model after update failed", err.Error())
		return
	}

	newState := registeredModelToState(ctx, info, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *registeredModelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state registeredModelResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteRegisteredModel(state.Name.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Delete registered model failed", err.Error())
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func registeredModelToState(_ context.Context, info *registeredModelInfo, _ *diag.Diagnostics) *registeredModelResourceModel {
	tagMap := make(map[string]attr.Value, len(info.Tags))
	for _, t := range info.Tags {
		tagMap[t.Key] = types.StringValue(t.Value)
	}
	tags, _ := types.MapValue(types.StringType, tagMap)

	return &registeredModelResourceModel{
		Name:        types.StringValue(info.Name),
		Description: types.StringValue(info.Description),
		Tags:        tags,
	}
}
