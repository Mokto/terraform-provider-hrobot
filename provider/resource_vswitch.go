package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/mokto/terraform-provider-hrobot/internal/client"
)

type vswitchResource struct {
	providerData *ProviderData
}

type vswitchModel struct {
	ID   types.Int64  `tfsdk:"id"`
	VLAN types.Int64  `tfsdk:"vlan"`
	Name types.String `tfsdk:"name"`
}

func NewResourceVSwitch() resource.Resource {
	return &vswitchResource{}
}

func (r *vswitchResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vswitch"
}

func (r *vswitchResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		Description: "Manages a Hetzner Robot virtual switch (vSwitch).",
		Attributes: map[string]rschema.Attribute{
			"id": rschema.Int64Attribute{
				Computed:    true,
				Description: "The unique ID of the vSwitch.",
			},
			"vlan": rschema.Int64Attribute{
				Required:    true,
				Description: "The VLAN ID for the vSwitch (e.g., 4000).",
			},
			"name": rschema.StringAttribute{
				Required:    true,
				Description: "The name of the vSwitch.",
			},
		},
	}
}

func (r *vswitchResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.providerData = req.ProviderData.(*ProviderData)
}

func (r *vswitchResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vswitchModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vswitch, err := r.providerData.Client.CreateVSwitch(int(plan.VLAN.ValueInt64()), plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to create vSwitch", err.Error())
		return
	}

	state := vswitchModel{
		ID:   types.Int64Value(int64(vswitch.ID)),
		VLAN: types.Int64Value(int64(vswitch.VLAN)),
		Name: types.StringValue(vswitch.Name),
	}

	tflog.Info(ctx, "Created vSwitch", map[string]interface{}{
		"id":   vswitch.ID,
		"vlan": vswitch.VLAN,
		"name": vswitch.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vswitchResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vswitchModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.State.RemoveResource(ctx)
		return
	}

	vswitch, err := r.providerData.Client.GetVSwitch(int(state.ID.ValueInt64()))
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read vSwitch", err.Error())
		return
	}

	state.VLAN = types.Int64Value(int64(vswitch.VLAN))
	state.Name = types.StringValue(vswitch.Name)

	tflog.Info(ctx, "Read vSwitch", map[string]interface{}{
		"id":   vswitch.ID,
		"vlan": vswitch.VLAN,
		"name": vswitch.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vswitchResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vswitchModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state vswitchModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError("Invalid vSwitch ID", "vSwitch ID is required for updates")
		return
	}

	vswitch, err := r.providerData.Client.UpdateVSwitch(int(state.ID.ValueInt64()), int(plan.VLAN.ValueInt64()), plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to update vSwitch", err.Error())
		return
	}

	state.VLAN = types.Int64Value(int64(vswitch.VLAN))
	state.Name = types.StringValue(vswitch.Name)

	tflog.Info(ctx, "Updated vSwitch", map[string]interface{}{
		"id":   vswitch.ID,
		"vlan": vswitch.VLAN,
		"name": vswitch.Name,
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vswitchResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vswitchModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.IsNull() || state.ID.IsUnknown() {
		tflog.Info(ctx, "vSwitch ID is null or unknown, skipping deletion")
		return
	}

	err := r.providerData.Client.DeleteVSwitch(int(state.ID.ValueInt64()))
	if err != nil {
		resp.Diagnostics.AddError("Failed to delete vSwitch", err.Error())
		return
	}

	tflog.Info(ctx, "Deleted vSwitch", map[string]interface{}{
		"id": state.ID.ValueInt64(),
	})
}

func (r *vswitchResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.Atoi(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid vSwitch ID", fmt.Sprintf("Expected integer, got: %s", req.ID))
		return
	}

	vswitch, err := r.providerData.Client.GetVSwitch(id)
	if err != nil {
		resp.Diagnostics.AddError("Failed to import vSwitch", err.Error())
		return
	}

	state := vswitchModel{
		ID:   types.Int64Value(int64(vswitch.ID)),
		VLAN: types.Int64Value(int64(vswitch.VLAN)),
		Name: types.StringValue(vswitch.Name),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
