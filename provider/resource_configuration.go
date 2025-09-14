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
	RobotName    types.String `tfsdk:"robot_name"`
	Description  types.String `tfsdk:"description"`
	VSwitchID    types.Int64  `tfsdk:"vswitch_id"`
	Version      types.Int64  `tfsdk:"version"`
	LocalIP      types.String `tfsdk:"local_ip"` // Now computed, automatically assigned
	RaidLevel    types.Int64  `tfsdk:"raid_level"`

	// Autosetup parameters
	Arch          types.String `tfsdk:"arch"`
	CryptPassword types.String `tfsdk:"cryptpassword"`
	ExtraScript   types.String `tfsdk:"extra_script"`
	NoUEFI        types.Bool   `tfsdk:"no_uefi"`

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
			"robot_name":    rschema.StringAttribute{Optional: true, Description: "Custom name for the server in Hetzner Robot interface (if not set, uses server_name)"},
			"description":   rschema.StringAttribute{Optional: true, Description: "Custom description for the server"},
			"vswitch_id":    rschema.Int64Attribute{Optional: true, Description: "ID of the vSwitch to connect the server to"},
			"version":       rschema.Int64Attribute{Optional: true, Description: "Version of the node, will trigger rescue + full install on each change"},
			"local_ip":      rschema.StringAttribute{Computed: true, Description: "Automatically assigned local IP address for private network configuration (10.1.0.2-10.1.0.127)"},
			"raid_level":    rschema.Int64Attribute{Optional: true, Description: "RAID level for software RAID configuration (default: 1)"},

			// Autosetup parameters
			"arch":          rschema.StringAttribute{Required: true, Description: "Architecture for the OS image (arm64 or amd64)"},
			"cryptpassword": rschema.StringAttribute{Required: true, Sensitive: true, Description: "Password for disk encryption (used in autosetup)"},
			"extra_script":  rschema.StringAttribute{Optional: true, Description: "Additional shell commands to run at the end of the postinstall first-run script"},
			"no_uefi":       rschema.BoolAttribute{Optional: true, Description: "If true, removes the UEFI boot partition from the disk partitioning scheme"},

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

	// Automatically assign a private IP
	localIP, err := r.providerData.GetNextAvailableIP()
	if err != nil {
		resp.Diagnostics.AddError("IP assignment failed", err.Error())
		return
	}
	plan.LocalIP = types.StringValue(localIP)

	tflog.Info(ctx, "assigned private IP", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"local_ip":      localIP,
	})

	// 1) Set server name if provided (use robot_name if set, otherwise use server_name)
	robotName := plan.ServerName.ValueString() // Default to server_name
	if !plan.RobotName.IsNull() && !plan.RobotName.IsUnknown() && plan.RobotName.ValueString() != "" {
		robotName = plan.RobotName.ValueString()
	}

	if robotName != "" {
		tflog.Info(ctx, "setting server name in Robot interface", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"robot_name":    robotName,
			"server_name":   plan.ServerName.ValueString(),
		})

		err := r.providerData.Client.SetServerName(int(plan.ServerNumber.ValueInt64()), robotName)
		if err != nil {
			resp.Diagnostics.AddError("set server name failed", err.Error())
			return
		}
		tflog.Info(ctx, "server name set successfully in Robot interface", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"robot_name":    robotName,
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
	err_summary, err_detail := r.configure(fp, ip, plan, ctx)
	if err_summary != "" {
		resp.Diagnostics.AddError(err_summary, err_detail)
		return
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

	// Preserve local_ip from current state - it should never change once assigned
	var currentState configurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &currentState)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !currentState.LocalIP.IsNull() && !currentState.LocalIP.IsUnknown() {
		plan.LocalIP = currentState.LocalIP
	}

	// Check if server name or robot name changed and update it
	robotName := plan.ServerName.ValueString() // Default to server_name
	if !plan.RobotName.IsNull() && !plan.RobotName.IsUnknown() && plan.RobotName.ValueString() != "" {
		robotName = plan.RobotName.ValueString()
	}

	if robotName != "" {
		err := r.providerData.Client.SetServerName(int(plan.ServerNumber.ValueInt64()), robotName)
		if err != nil {
			resp.Diagnostics.AddError("update server name failed", err.Error())
			return
		}
		tflog.Info(ctx, "updated server name in Robot interface", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"robot_name":    robotName,
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
		// Get current state to preserve or release IP
		var versionCurrentState configurationModel
		resp.Diagnostics.Append(req.State.Get(ctx, &versionCurrentState)...)
		if resp.Diagnostics.HasError() {
			return
		}

		// Preserve the existing IP assignment for version changes
		if !versionCurrentState.LocalIP.IsNull() && !versionCurrentState.LocalIP.IsUnknown() && versionCurrentState.LocalIP.ValueString() != "" {
			plan.LocalIP = versionCurrentState.LocalIP
		} else {
			// Assign new IP if none exists
			localIP, ipErr := r.providerData.GetNextAvailableIP()
			if ipErr != nil {
				resp.Diagnostics.AddError("IP assignment failed", ipErr.Error())
				return
			}
			plan.LocalIP = types.StringValue(localIP)
		}

		summary, err_detail := r.configure(mustStringSliceUpdate(ctx, resp, plan.RescueKeyFPs), plan.ServerIP.ValueString(), plan, ctx)
		if summary != "" {
			resp.Diagnostics.AddError(summary, err_detail)
			return
		}
		tflog.Info(ctx, "reconfigured server due to version change", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"version":       plan.Version.ValueInt64(),
		})

		// Update state with the new plan values, preserving ID from current state
		var versionUpdateState configurationModel
		resp.Diagnostics.Append(req.State.Get(ctx, &versionUpdateState)...)
		if resp.Diagnostics.HasError() {
			return
		}

		state := plan
		state.ID = versionUpdateState.ID // Preserve existing ID
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	// For other changes that don't require reconfiguration, update the state, preserving ID
	state := plan
	state.ID = currentState.ID // Preserve existing ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)

	// Note: Some changes may require recreation (taint/recreate)
	if resp.Diagnostics.HasError() {
		resp.Diagnostics.AddWarning("Update limited", "Some changes may require resource recreation (taint/recreate).")
	}
}

func (r *configurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state configurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Release the private IP if one was assigned
	if !state.LocalIP.IsNull() && !state.LocalIP.IsUnknown() && state.LocalIP.ValueString() != "" {
		r.providerData.ReleaseIP(state.LocalIP.ValueString())
		tflog.Info(ctx, "released private IP", map[string]interface{}{
			"server_number": state.ServerNumber.ValueInt64(),
			"local_ip":      state.LocalIP.ValueString(),
		})
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
