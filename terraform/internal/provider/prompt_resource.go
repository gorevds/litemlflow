// prompt_resource.go — Terraform resource for litemlflow_prompt.
//
// Design note: prompts are versioned and content-addressed. POST /api/v1/prompts
// with identical content reuses the existing version. There is no in-place edit
// (Update maps to a new POST, which may return the same version if content is
// unchanged, or a new version number if it changed). This is why the resource
// has no true Update — the "update" behaviour is to create a new version.
// Terraform will track the latest version number in state.
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

var _ resource.Resource = &promptResource{}

func newPromptResource() resource.Resource { return &promptResource{} }

type promptResource struct{ client *Client }

type promptResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Content     types.String `tfsdk:"content"`
	Description types.String `tfsdk:"description"`
	Version     types.Int64  `tfsdk:"version"`
	ContentHash types.String `tfsdk:"content_hash"`
}

func (r *promptResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_prompt"
}

func (r *promptResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a versioned LiteMLflow prompt (maps to `POST /api/v1/prompts`). " +
			"Prompts are content-addressed and append-only: changing `content` creates a new version. " +
			"There is no in-place edit — the resource tracks the latest version number.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Stable identifier (`<name>@<version>`) stored in Terraform state.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Prompt name (e.g. `rag.system`). Immutable after creation; renaming requires destroying and recreating.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"content": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Prompt text content. Changing this value causes a new version to be created on the server.",
			},
			"description": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Human-readable description of the prompt.",
			},
			"version": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "The version number assigned by the server after the most recent `apply`.",
			},
			"content_hash": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SHA-256 content hash assigned by the server.",
			},
		},
	}
}

func (r *promptResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *promptResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan promptResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	version, hash, err := r.client.CreatePrompt(
		plan.Name.ValueString(),
		plan.Content.ValueString(),
		plan.Description.ValueString(),
		nil,
	)
	if err != nil {
		resp.Diagnostics.AddError("Create prompt failed", err.Error())
		return
	}

	state := promptResourceModel{
		ID:          types.StringValue(fmt.Sprintf("%s@%d", plan.Name.ValueString(), version)),
		Name:        plan.Name,
		Content:     plan.Content,
		Description: plan.Description,
		Version:     types.Int64Value(int64(version)),
		ContentHash: types.StringValue(hash),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *promptResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state promptResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	info, err := r.client.GetPromptVersion(state.Name.ValueString(), int(state.Version.ValueInt64()))
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read prompt failed", err.Error())
		return
	}

	newState := promptResourceModel{
		ID:          types.StringValue(fmt.Sprintf("%s@%d", info.Name, info.Version)),
		Name:        types.StringValue(info.Name),
		Content:     types.StringValue(info.Content),
		Description: types.StringValue(info.Description),
		Version:     types.Int64Value(int64(info.Version)),
		ContentHash: types.StringValue(info.ContentHash),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update is called when content or description changes. Since prompts are
// append-only, we POST again (possibly creating a new version) and update state.
func (r *promptResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan promptResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	version, hash, err := r.client.CreatePrompt(
		plan.Name.ValueString(),
		plan.Content.ValueString(),
		plan.Description.ValueString(),
		nil,
	)
	if err != nil {
		resp.Diagnostics.AddError("Update prompt (new version) failed", err.Error())
		return
	}

	state := promptResourceModel{
		ID:          types.StringValue(fmt.Sprintf("%s@%d", plan.Name.ValueString(), version)),
		Name:        plan.Name,
		Content:     plan.Content,
		Description: plan.Description,
		Version:     types.Int64Value(int64(version)),
		ContentHash: types.StringValue(hash),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Delete removes the tracked prompt version (best-effort — server may not
// support version deletion; the call is silently ignored on 404/405).
func (r *promptResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state promptResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeletePromptVersion(state.Name.ValueString(), int(state.Version.ValueInt64())); err != nil {
		resp.Diagnostics.AddError("Delete prompt version failed", err.Error())
	}
}
