// workspace_resource.go — Terraform resource for litemlflow_workspace.
// Maps to /api/v1/workspaces/* (native LiteMLflow API).
package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &workspaceResource{}

func newWorkspaceResource() resource.Resource { return &workspaceResource{} }

type workspaceResource struct{ client *Client }

type workspaceResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
}

func (r *workspaceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace"
}

func (r *workspaceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a LiteMLflow workspace (maps to `/api/v1/workspaces/*`). " +
			"Workspace IDs are slugs (`[a-z0-9-]{1,64}`) and are immutable after creation. " +
			"The `default` workspace cannot be deleted.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Workspace slug ID (e.g. `team-nlp`). Immutable after creation.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable workspace display name.",
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Optional free-text description.",
			},
		},
	}
}

func (r *workspaceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *workspaceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workspaceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	info, err := r.client.CreateWorkspace(plan.ID.ValueString(), plan.Name.ValueString(), plan.Description.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Create workspace failed", err.Error())
		return
	}

	state := workspaceResourceModel{
		ID:          types.StringValue(info.ID),
		Name:        types.StringValue(info.Name),
		Description: types.StringValue(info.Description),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *workspaceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workspaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	info, err := r.client.GetWorkspace(state.ID.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read workspace failed", err.Error())
		return
	}

	newState := workspaceResourceModel{
		ID:          types.StringValue(info.ID),
		Name:        types.StringValue(info.Name),
		Description: types.StringValue(info.Description),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *workspaceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state workspaceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	info, err := r.client.UpdateWorkspace(state.ID.ValueString(), plan.Name.ValueString(), plan.Description.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Update workspace failed", err.Error())
		return
	}

	newState := workspaceResourceModel{
		ID:          types.StringValue(info.ID),
		Name:        types.StringValue(info.Name),
		Description: types.StringValue(info.Description),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

func (r *workspaceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workspaceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeleteWorkspace(state.ID.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Delete workspace failed", err.Error())
	}
}
