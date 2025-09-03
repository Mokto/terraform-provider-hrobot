package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/mokto/terraform-provider-hrobot/internal/client"
	sshx "github.com/mokto/terraform-provider-hrobot/internal/ssh"
)

type configurationResource struct{ providerData *ProviderData }

type configurationModel struct {
	ID           types.String `tfsdk:"id"`
	ServerNumber types.Int64  `tfsdk:"server_number"`
	ServerIP     types.String `tfsdk:"server_ip"`
	ServerName   types.String `tfsdk:"server_name"`

	Autosetup   types.String `tfsdk:"autosetup_content"`
	PostInstall types.String `tfsdk:"post_install_content"`

	AnsibleRepo     types.String `tfsdk:"ansible_repo"`
	AnsiblePlaybook types.String `tfsdk:"ansible_playbook"`
	AnsibleExtra    types.String `tfsdk:"ansible_extra"`

	RescueKeyFPs   types.List  `tfsdk:"rescue_authorized_key_fingerprints"`
	SSHWaitMinutes types.Int64 `tfsdk:"ssh_wait_timeout_minutes"`
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
			"server_ip":     rschema.StringAttribute{Computed: true, Description: "The server's IP address"},
			"server_name":   rschema.StringAttribute{Optional: true, Description: "Custom name for the server"},

			"autosetup_content":    rschema.StringAttribute{Required: true, Sensitive: true, Description: "Autosetup configuration content"},
			"post_install_content": rschema.StringAttribute{Optional: true, Sensitive: true, Description: "Post-install script content"},

			"ansible_repo":     rschema.StringAttribute{Optional: true, Description: "Ansible repository URL for post-install automation"},
			"ansible_playbook": rschema.StringAttribute{Optional: true, Computed: true, Description: "Ansible playbook to run (defaults to site.yml)"},
			"ansible_extra":    rschema.StringAttribute{Optional: true, Computed: true, Description: "Extra Ansible variables"},

			"rescue_authorized_key_fingerprints": rschema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "SSH key fingerprints for rescue mode access",
			},
			"ssh_wait_timeout_minutes": rschema.Int64Attribute{
				Optional: true, Computed: true, Description: "Timeout waiting for SSH to be available",
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

	fp := mustStringSlice(ctx, resp, plan.RescueKeyFPs)
	if resp.Diagnostics.HasError() {
		return
	}

	// 1) Set server name if provided
	if !plan.ServerName.IsNull() && !plan.ServerName.IsUnknown() && plan.ServerName.ValueString() != "" {
		err := r.providerData.Client.SetServerName(int(plan.ServerNumber.ValueInt64()), plan.ServerName.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("set server name failed", err.Error())
			return
		}
		tflog.Info(ctx, "set server name", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"server_name":   plan.ServerName.ValueString(),
		})
	}

	// 2) Activate Rescue
	rescue, err := r.providerData.Client.ActivateRescue(int(plan.ServerNumber.ValueInt64()), client.RescueParams{
		OS:            "linux",
		AuthorizedFPs: fp,
	})
	if err != nil {
		resp.Diagnostics.AddError("activate rescue failed", err.Error())
		return
	}
	ip := rescue.ServerIP

	// 3) Reset into Rescue
	if err := r.providerData.Client.Reset(int(plan.ServerNumber.ValueInt64()), "hw"); err != nil {
		resp.Diagnostics.AddError("reset failed", err.Error())
		return
	}

	// 4) Wait for SSH
	waitMin := int64(20)
	if !plan.SSHWaitMinutes.IsNull() && !plan.SSHWaitMinutes.IsUnknown() && plan.SSHWaitMinutes.ValueInt64() > 0 {
		waitMin = plan.SSHWaitMinutes.ValueInt64()
	}
	if err := waitTCP(ip+":22", time.Duration(waitMin)*time.Minute); err != nil {
		resp.Diagnostics.AddError("rescue ssh timeout", err.Error())
		return
	}

	// 5) SSH/SFTP upload
	var auth sshx.Auth
	if len(fp) > 0 {
		auth = sshx.AuthFromAgent()
	} else {
		auth = sshx.AuthPassword(rescue.Password)
	}
	conn, closeFn, err := sshx.Connect(sshx.Conn{Host: ip, User: "root", Timeout: 3 * time.Minute, Auth: auth, InsecureIgnoreHostKey: true})
	if err != nil {
		resp.Diagnostics.AddError("ssh connect", err.Error())
		return
	}
	defer closeFn()

	if err := sshx.Upload(conn, "/autosetup", []byte(plan.Autosetup.ValueString()), 0600); err != nil {
		resp.Diagnostics.AddError("upload autosetup", err.Error())
		return
	}

	post := plan.PostInstall.ValueString()
	if post == "" && !plan.AnsibleRepo.IsNull() && plan.AnsibleRepo.ValueString() != "" {
		play := "site.yml"
		extra := ""
		if !plan.AnsiblePlaybook.IsNull() {
			play = plan.AnsiblePlaybook.ValueString()
		}
		if !plan.AnsibleExtra.IsNull() {
			extra = plan.AnsibleExtra.ValueString()
		}
		post = fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if command -v apt-get >/dev/null 2>&1; then apt-get update -y && apt-get install -y git ansible || true; fi
ansible-pull -U %s -i localhost, -e '%s' %s || true
`, plan.AnsibleRepo.ValueString(), extra, play)
	}
	if post != "" {
		if err := sshx.Upload(conn, "/root/post-install.sh", []byte(post), 0700); err != nil {
			resp.Diagnostics.AddError("upload post-install", err.Error())
			return
		}
		_, _ = sshx.Run(conn, "chmod +x /root/post-install.sh || true")
	}

	// 6) Run installimage and reboot
	if _, err := sshx.Run(conn, "installimage -a /autosetup"); err != nil {
		resp.Diagnostics.AddError("installimage failed", err.Error())
		return
	}
	_, _ = sshx.Run(conn, "reboot || systemctl reboot || shutdown -r now || true")

	// 7) Wait for OS SSH to come back
	if err := waitTCP(ip+":22", time.Duration(waitMin)*time.Minute); err != nil {
		// give a little more
		if err2 := waitTCP(ip+":22", 15*time.Minute); err2 != nil {
			resp.Diagnostics.AddError("os ssh timeout", fmt.Sprintf("%v / %v", err, err2))
			return
		}
	}

	state := plan
	state.ID = types.StringValue(fmt.Sprintf("configuration-%d", time.Now().Unix()))
	state.ServerIP = types.StringValue(ip)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	tflog.Info(ctx, "configuration finished", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_name":   plan.ServerName.ValueString(),
		"ip":            ip,
	})
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

	// For other changes, we need to recreate the resource
	resp.Diagnostics.AddWarning("Update limited", "Only server name can be updated. Other changes require recreation (taint/recreate).")
}

func (r *configurationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Configuration is a one-shot action, no destructive cleanup needed
	tflog.Info(ctx, "configuration resource deleted from state")
}
