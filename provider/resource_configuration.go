package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

type nodeLabelModel struct {
	Name  types.String `tfsdk:"name"`
	Value types.String `tfsdk:"value"`
}

type configurationResource struct{ providerData *ProviderData }

type configurationModel struct {
	ID           types.String `tfsdk:"id"`
	ServerNumber types.Int64  `tfsdk:"server_number"`
	ServerIP     types.String `tfsdk:"server_ip"`
	Name         types.String `tfsdk:"name"`
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
	NoUEFI        types.Bool   `tfsdk:"no_uefi"`

	// K3S parameters
	K3SToken   types.String `tfsdk:"k3s_token"`
	K3SURL     types.String `tfsdk:"k3s_url"`
	NodeLabels types.List   `tfsdk:"node_labels"`
	Taints     types.List   `tfsdk:"taints"`

	RescueKeyFPs types.List `tfsdk:"rescue_authorized_key_fingerprints"`
}

// generateNameHash generates a 6-character alphanumeric hash based on name, server number, and version
func generateNameHash(name string, serverNumber int64, version int64) (string, error) {
	// Generate random bytes
	bytes := make([]byte, 3) // 3 bytes = 6 hex characters
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	// The hash changes only on creation and version changes (random generation)
	hash := hex.EncodeToString(bytes)
	return hash, nil
}

// computeNames generates server_name and robot_name from base name and hash
func computeNames(name string, hash string) (string, string) {
	computedName := fmt.Sprintf("%s-%s", name, hash)
	return computedName, computedName
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
			"name":          rschema.StringAttribute{Required: true, Description: "Base name for the server (server_name and robot_name will be computed as name-{6-char-id})"},
			"server_name":   rschema.StringAttribute{Computed: true, Description: "Computed server name in format: name-{6-char-id} (used as hostname in autosetup)"},
			"robot_name":    rschema.StringAttribute{Computed: true, Description: "Computed robot name in format: name-{6-char-id} (used in Hetzner Robot interface)"},
			"description":   rschema.StringAttribute{Optional: true, Description: "Custom description for the server"},
			"vswitch_id":    rschema.Int64Attribute{Optional: true, Description: "ID of the vSwitch to connect the server to"},
			"version":       rschema.Int64Attribute{Optional: true, Description: "Version of the node, will trigger rescue + full install on each change"},
			"local_ip":      rschema.StringAttribute{Computed: true, Description: "Automatically assigned local IP address for private network configuration (10.1.0.2-10.1.0.127)"},
			"raid_level":    rschema.Int64Attribute{Optional: true, Description: "RAID level for software RAID configuration (default: 1)"},

			// Autosetup parameters
			"arch":          rschema.StringAttribute{Required: true, Description: "Architecture for the OS image (arm64 or amd64)"},
			"cryptpassword": rschema.StringAttribute{Required: true, Sensitive: true, Description: "Password for disk encryption (used in autosetup)"},
			"no_uefi":       rschema.BoolAttribute{Optional: true, Description: "If true, removes the UEFI boot partition from the disk partitioning scheme"},

			// K3S parameters
			"k3s_token": rschema.StringAttribute{Required: true, Sensitive: true, Description: "K3S token for joining the cluster"},
			"k3s_url":   rschema.StringAttribute{Required: true, Description: "K3S server URL (e.g., https://master-ip:6443)"},
			"node_labels": rschema.ListNestedAttribute{
				Optional:    true,
				Description: "List of node labels to apply to this K3S node",
				NestedObject: rschema.NestedAttributeObject{
					Attributes: map[string]rschema.Attribute{
						"name":  rschema.StringAttribute{Required: true, Description: "Label name"},
						"value": rschema.StringAttribute{Required: true, Description: "Label value"},
					},
				},
			},
			"taints": rschema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "List of taints to apply to this K3S node (e.g., 'localstorage=true:NoSchedule')",
			},

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

	// Generate hash for computed names
	version := int64(1) // Default version for new resources
	if !plan.Version.IsNull() && !plan.Version.IsUnknown() {
		version = plan.Version.ValueInt64()
	}

	nameHash, err := generateNameHash(plan.Name.ValueString(), plan.ServerNumber.ValueInt64(), version)
	if err != nil {
		resp.Diagnostics.AddError("Failed to generate name hash", err.Error())
		return
	}

	// Compute server_name and robot_name
	serverName, robotName := computeNames(plan.Name.ValueString(), nameHash)
	plan.ServerName = types.StringValue(serverName)
	plan.RobotName = types.StringValue(robotName)

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

	// Set computed robot name in Hetzner Robot interface
	tflog.Info(ctx, "setting computed server name in Robot interface", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"robot_name":    plan.RobotName.ValueString(),
		"server_name":   plan.ServerName.ValueString(),
	})

	err = r.providerData.Client.SetServerName(int(plan.ServerNumber.ValueInt64()), plan.RobotName.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("set server name failed", err.Error())
		return
	}
	tflog.Info(ctx, "computed server name set successfully in Robot interface", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"robot_name":    plan.RobotName.ValueString(),
	})
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

	// Get current state to check for changes
	var currentState configurationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &currentState)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Preserve local_ip from current state - it should never change once assigned
	if !currentState.LocalIP.IsNull() && !currentState.LocalIP.IsUnknown() {
		plan.LocalIP = currentState.LocalIP
	}

	// Check if name or version changed - if so, regenerate the hash and names
	nameChanged := !currentState.Name.IsNull() && plan.Name.ValueString() != currentState.Name.ValueString()
	versionChanged := !plan.Version.IsNull() && !plan.Version.IsUnknown() &&
		(currentState.Version.IsNull() || plan.Version.ValueInt64() != currentState.Version.ValueInt64())

	if nameChanged || versionChanged {
		// Generate new hash for updated name/version
		version := int64(1)
		if !plan.Version.IsNull() && !plan.Version.IsUnknown() {
			version = plan.Version.ValueInt64()
		}

		nameHash, err := generateNameHash(plan.Name.ValueString(), plan.ServerNumber.ValueInt64(), version)
		if err != nil {
			resp.Diagnostics.AddError("Failed to generate name hash", err.Error())
			return
		}

		// Compute new server_name and robot_name
		serverName, robotName := computeNames(plan.Name.ValueString(), nameHash)
		plan.ServerName = types.StringValue(serverName)
		plan.RobotName = types.StringValue(robotName)
	} else {
		// Preserve existing computed names if name and version didn't change
		plan.ServerName = currentState.ServerName
		plan.RobotName = currentState.RobotName
	}

	// Update server name in Robot interface
	if !plan.RobotName.IsNull() && !plan.RobotName.IsUnknown() {
		err := r.providerData.Client.SetServerName(int(plan.ServerNumber.ValueInt64()), plan.RobotName.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("update server name failed", err.Error())
			return
		}
		tflog.Info(ctx, "updated computed server name in Robot interface", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"robot_name":    plan.RobotName.ValueString(),
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

		r.providerData.Client.SetServerName(serverNumber, "cancelled")

	} else {
		// No server number available, just remove from state
		tflog.Info(ctx, "Removing configuration from state (no server number available)")

		resp.Diagnostics.AddWarning(
			"Manual Cancellation May Be Required",
			"The configuration has been removed from Terraform state, but if a server was created, you may need to cancel it manually through the Hetzner Robot interface.",
		)
	}
}
