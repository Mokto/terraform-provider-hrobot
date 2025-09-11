package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

type configurationResource struct{ providerData *ProviderData }

type configurationModel struct {
	ID           types.String `tfsdk:"id"`
	ServerNumber types.Int64  `tfsdk:"server_number"`
	ServerIP     types.String `tfsdk:"server_ip"`
	ServerName   types.String `tfsdk:"server_name"`
	Description  types.String `tfsdk:"description"`
	VSwitchID    types.Int64  `tfsdk:"vswitch_id"`
	Version      types.Int64  `tfsdk:"version"`

	// Autosetup parameters
	Arch          types.String `tfsdk:"arch"`
	CryptPassword types.String `tfsdk:"cryptpassword"`

	RescueKeyFPs types.List `tfsdk:"rescue_authorized_key_fingerprints"`
}

func NewResourceConfiguration() resource.Resource { return &configurationResource{} }

func (r *configurationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_configuration"
}

func (r *configurationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		Description: "Manages Hetzner Robot server configuration including server naming, OS installation, and post-install setup.",
		Attributes: map[string]rschema.Attribute{
			"server_number": rschema.Int64Attribute{Required: true, Description: "Robot server number"},
			"server_ip":     rschema.StringAttribute{Required: true, Description: "The server's IP address"},
			"server_name":   rschema.StringAttribute{Required: true, Description: "Custom name for the server (used as hostname in autosetup)"},
			"description":   rschema.StringAttribute{Optional: true, Description: "Custom description for the server"},
			"vswitch_id":    rschema.Int64Attribute{Optional: true, Description: "ID of the vSwitch to connect the server to"},
			"version":       rschema.Int64Attribute{Optional: true, Description: "Version of the node, will trigger rescue + full install on each change"},

			// Autosetup parameters
			"arch":          rschema.StringAttribute{Required: true, Description: "Architecture for the OS image (arm64 or amd64)"},
			"cryptpassword": rschema.StringAttribute{Required: true, Sensitive: true, Description: "Password for disk encryption (used in autosetup)"},

			"rescue_authorized_key_fingerprints": rschema.ListAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "SSH key fingerprints for rescue mode access",
			},
			"id": rschema.StringAttribute{Computed: true},
		},
	}
}

func (r *configurationResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.providerData = req.ProviderData.(*ProviderData)
}

func (r *configurationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan configurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fp := mustStringSliceCreate(ctx, resp, plan.RescueKeyFPs)
	if resp.Diagnostics.HasError() {
		return
	}

	ip := plan.ServerIP.ValueString()

	// 1) Set server name if provided
	if !plan.ServerName.IsNull() && !plan.ServerName.IsUnknown() && plan.ServerName.ValueString() != "" {
		tflog.Info(ctx, "setting server name", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"server_name":   plan.ServerName.ValueString(),
		})

		err := r.providerData.Client.SetServerName(int(plan.ServerNumber.ValueInt64()), plan.ServerName.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("set server name failed", err.Error())
			return
		}
		tflog.Info(ctx, "server name set successfully", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"server_name":   plan.ServerName.ValueString(),
		})
	}
	//
	//
	// Add server to vswitch if provided
	if !plan.VSwitchID.IsNull() && !plan.VSwitchID.IsUnknown() {
		serverIP := plan.ServerIP.ValueString()

		tflog.Info(ctx, "adding server to vswitch", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"server_ip":     serverIP,
			"vswitch_id":    plan.VSwitchID.ValueInt64(),
		})

		err := r.providerData.Client.AddServerToVSwitch(int(plan.VSwitchID.ValueInt64()), serverIP)
		if err != nil {
			resp.Diagnostics.AddError("add server to vswitch failed", err.Error())
			return
		}

		tflog.Info(ctx, "server added to vswitch successfully", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"server_ip":     serverIP,
			"vswitch_id":    plan.VSwitchID.ValueInt64(),
		})
	}

	// Configure
	err_summary, err := r.configure(fp, ip, plan, ctx)
	if err_summary != "" {
		resp.Diagnostics.AddError(err_summary, err)
	}

	state := plan
	state.ID = types.StringValue(fmt.Sprintf("configuration-%d", time.Now().Unix()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *configurationResource) Read(_ context.Context, _ resource.ReadRequest, _ *resource.ReadResponse) {
	// Configuration is a one-shot action, no state to read
}

func (r *configurationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan configurationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Check if server name changed and update it
	if !plan.ServerName.IsNull() && !plan.ServerName.IsUnknown() && plan.ServerName.ValueString() != "" {
		err := r.providerData.Client.SetServerName(int(plan.ServerNumber.ValueInt64()), plan.ServerName.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("update server name failed", err.Error())
			return
		}
		tflog.Info(ctx, "updated server name", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"server_name":   plan.ServerName.ValueString(),
		})
	}

	// Check if vswitch changed and update it
	if !plan.VSwitchID.IsNull() && !plan.VSwitchID.IsUnknown() {
		// Get current server IP from state
		var state configurationModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}

		if !state.ServerIP.IsNull() && !state.ServerIP.IsUnknown() {
			err := r.providerData.Client.AddServerToVSwitch(int(plan.VSwitchID.ValueInt64()), state.ServerIP.ValueString())
			if err != nil {
				resp.Diagnostics.AddError("update server vswitch failed", err.Error())
				return
			}
			tflog.Info(ctx, "updated server vswitch", map[string]interface{}{
				"server_number": plan.ServerNumber.ValueInt64(),
				"server_ip":     state.ServerIP.ValueString(),
				"vswitch_id":    plan.VSwitchID.ValueInt64(),
			})
		}
	}

	if !plan.Version.IsNull() && !plan.Version.IsUnknown() {
		summary, err := r.configure(mustStringSliceUpdate(ctx, resp, plan.RescueKeyFPs), plan.ServerIP.ValueString(), plan, ctx)
		if err != "" {
			resp.Diagnostics.AddError(summary, err)
			return
		}
		tflog.Info(ctx, "reconfigured server due to version change", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"version":       plan.Version.ValueInt64(),
		})
		resp.Diagnostics.AddWarning("Reconfiguration completed", "The server has been reconfigured due to a version change. Note that this does not update the version in the state; please ensure the version is updated in the Terraform configuration if needed.")
		return
	}

	// For other changes, we need to recreate the resource
	resp.Diagnostics.AddWarning("Update limited", "Only server name and vswitch can be updated. Other changes require recreation (taint/recreate).")
}

func (r *configurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state configurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// If we have a server number, schedule cancellation at the end of billing period
	if !state.ServerNumber.IsNull() && !state.ServerNumber.IsUnknown() {
		serverNumber := int(state.ServerNumber.ValueInt64())

		// Schedule cancellation at the end of the billing period (empty cancelDate means end of period)
		err := r.providerData.Client.CancelServer(serverNumber, "")
		if err != nil {
			// Log the error but don't fail the delete operation
			// The server will be removed from Terraform state regardless
			tflog.Warn(ctx, "Failed to schedule server cancellation", map[string]interface{}{
				"server_number": serverNumber,
				"error":         err.Error(),
			})

			resp.Diagnostics.AddWarning(
				"Server Cancellation Failed",
				fmt.Sprintf("Failed to schedule cancellation for server %d: %s. Please cancel the server manually through the Hetzner Robot interface to stop billing.", serverNumber, err.Error()),
			)
		} else {
			tflog.Info(ctx, "Scheduled server cancellation", map[string]interface{}{
				"server_number": serverNumber,
			})

			resp.Diagnostics.AddWarning(
				"Server Cancellation Scheduled",
				fmt.Sprintf("Server %d has been scheduled for cancellation at the end of the billing period. The server will remain active until then.", serverNumber),
			)
		}
	} else {
		// No server number available, just remove from state
		tflog.Info(ctx, "Removing configuration from state (no server number available)")

		resp.Diagnostics.AddWarning(
			"Manual Cancellation May Be Required",
			"The configuration has been removed from Terraform state, but if a server was created, you may need to cancel it manually through the Hetzner Robot interface.",
		)
	}
}
