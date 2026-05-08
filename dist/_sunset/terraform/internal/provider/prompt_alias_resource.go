// prompt_alias_resource.go — Terraform resource for litemlflow_prompt_alias.
// Maps to POST/GET/DELETE /api/v1/prompts/{name}/aliases/*.
// An alias is a mutable pointer (e.g. "production") to a specific version number.
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

var _ resource.Resource = &promptAliasResource{}

func newPromptAliasResource() resource.Resource { return &promptAliasResource{} }

type promptAliasResource struct{ client *Client }

type promptAliasResourceModel struct {
	ID      types.String `tfsdk:"id"`
	Name    types.String `tfsdk:"name"`
	Alias   types.String `tfsdk:"alias"`
	Version types.Int64  `tfsdk:"version"`
}

func (r *promptAliasResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_prompt_alias"
}

func (r *promptAliasResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a prompt alias (e.g. `production → version 3`). " +
			"Alias upsert is idempotent — pointing the alias at a different version is an in-place update.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Composite ID `<name>/<alias>` stored in Terraform state.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Prompt name the alias belongs to.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"alias": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Alias label (e.g. `production`, `staging`).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"version": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "Version number this alias should point to.",
			},
		},
	}
}

func (r *promptAliasResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *promptAliasResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan promptAliasResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.SetPromptAlias(plan.Name.ValueString(), plan.Alias.ValueString(), int(plan.Version.ValueInt64())); err != nil {
		resp.Diagnostics.AddError("Set prompt alias failed", err.Error())
		return
	}

	plan.ID = types.StringValue(fmt.Sprintf("%s/%s", plan.Name.ValueString(), plan.Alias.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *promptAliasResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state promptAliasResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	info, err := r.client.GetPromptAlias(state.Name.ValueString(), state.Alias.ValueString())
	if err != nil {
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Read prompt alias failed", err.Error())
		return
	}

	newState := promptAliasResourceModel{
		ID:      types.StringValue(fmt.Sprintf("%s/%s", info.Name, info.Alias)),
		Name:    types.StringValue(info.Name),
		Alias:   types.StringValue(info.Alias),
		Version: types.Int64Value(int64(info.Version)),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
}

// Update re-points the alias to a different version (upsert semantics).
func (r *promptAliasResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan promptAliasResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.SetPromptAlias(plan.Name.ValueString(), plan.Alias.ValueString(), int(plan.Version.ValueInt64())); err != nil {
		resp.Diagnostics.AddError("Update prompt alias failed", err.Error())
		return
	}

	plan.ID = types.StringValue(fmt.Sprintf("%s/%s", plan.Name.ValueString(), plan.Alias.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *promptAliasResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state promptAliasResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.DeletePromptAlias(state.Name.ValueString(), state.Alias.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Delete prompt alias failed", err.Error())
	}
}
