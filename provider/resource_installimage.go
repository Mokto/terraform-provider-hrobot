package provider

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/mokto/terraform-provider-hrobot/internal/client"
	sshx "github.com/mokto/terraform-provider-hrobot/internal/ssh"
)

type installImageResource struct{ api *client.Client }

type installImageModel struct {
	ID           types.String `tfsdk:"id"`
	ServerNumber types.Int64  `tfsdk:"server_number"`
	ServerIP     types.String `tfsdk:"server_ip"`

	Autosetup   types.String `tfsdk:"autosetup_content"`
	PostInstall types.String `tfsdk:"post_install_content"`

	AnsibleRepo     types.String `tfsdk:"ansible_repo"`
	AnsiblePlaybook types.String `tfsdk:"ansible_playbook"`
	AnsibleExtra    types.String `tfsdk:"ansible_extra"`

	RescueKeyFPs   types.List  `tfsdk:"rescue_authorized_key_fingerprints"`
	SSHWaitMinutes types.Int64 `tfsdk:"ssh_wait_timeout_minutes"`
}

func NewResourceInstallImage() resource.Resource { return &installImageResource{} }

func (r *installImageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_installimage"
}

func (r *installImageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = rschema.Schema{
		Attributes: map[string]rschema.Attribute{
			"server_number": rschema.Int64Attribute{Required: true, Description: "Robot server number"},
			"server_ip":     rschema.StringAttribute{Computed: true},

			"autosetup_content":    rschema.StringAttribute{Required: true, Sensitive: true},
			"post_install_content": rschema.StringAttribute{Optional: true, Sensitive: true},

			"ansible_repo":     rschema.StringAttribute{Optional: true},
			"ansible_playbook": rschema.StringAttribute{Optional: true, Computed: true},
			"ansible_extra":    rschema.StringAttribute{Optional: true, Computed: true},

			"rescue_authorized_key_fingerprints": rschema.ListAttribute{
				Optional: true, ElementType: types.StringType,
			},
			"ssh_wait_timeout_minutes": rschema.Int64Attribute{
				Optional: true, Computed: true, Description: "Timeout waiting for SSH up",
			},
			"id": rschema.StringAttribute{Computed: true},
		},
	}
}

func (r *installImageResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.api = req.ProviderData.(*client.Client)
}

func (r *installImageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan installImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fp := mustStringSlice(ctx, resp, plan.RescueKeyFPs)
	if resp.Diagnostics.HasError() {
		return
	}

	// 1) Activate Rescue
	rescue, err := r.api.ActivateRescue(int(plan.ServerNumber.ValueInt64()), client.RescueParams{
		OS:            "linux",
		AuthorizedFPs: fp,
	})
	if err != nil {
		resp.Diagnostics.AddError("activate rescue failed", err.Error())
		return
	}
	ip := rescue.ServerIP

	// 2) Reset into Rescue
	if err := r.api.Reset(int(plan.ServerNumber.ValueInt64()), "hw"); err != nil {
		resp.Diagnostics.AddError("reset failed", err.Error())
		return
	}

	// 3) Wait for SSH
	waitMin := int64(20)
	if !plan.SSHWaitMinutes.IsNull() && !plan.SSHWaitMinutes.IsUnknown() && plan.SSHWaitMinutes.ValueInt64() > 0 {
		waitMin = plan.SSHWaitMinutes.ValueInt64()
	}
	if err := waitTCP(ip+":22", time.Duration(waitMin)*time.Minute); err != nil {
		resp.Diagnostics.AddError("rescue ssh timeout", err.Error())
		return
	}

	// 4) SSH/SFTP upload
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

	// 5) Run installimage and reboot
	if _, err := sshx.Run(conn, "installimage -a /autosetup"); err != nil {
		resp.Diagnostics.AddError("installimage failed", err.Error())
		return
	}
	_, _ = sshx.Run(conn, "reboot || systemctl reboot || shutdown -r now || true")

	// 6) Wait for OS SSH to come back
	if err := waitTCP(ip+":22", time.Duration(waitMin)*time.Minute); err != nil {
		// give a little more
		if err2 := waitTCP(ip+":22", 15*time.Minute); err2 != nil {
			resp.Diagnostics.AddError("os ssh timeout", fmt.Sprintf("%v / %v", err, err2))
			return
		}
	}

	state := plan
	state.ID = types.StringValue(fmt.Sprintf("installimage-%d", time.Now().Unix()))
	state.ServerIP = types.StringValue(ip)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	tflog.Info(ctx, "installimage finished", map[string]interface{}{"server_number": plan.ServerNumber.ValueInt64(), "ip": ip})
}

func (r *installImageResource) Read(_ context.Context, _ resource.ReadRequest, _ *resource.ReadResponse) {
}
func (r *installImageResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddWarning("Update ignored", "installimage is a one-shot action; change arguments to force new run (taint/recreate).")
}
func (r *installImageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// no destructive action; just remove from state
}
func waitTCP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}
