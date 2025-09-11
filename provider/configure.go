package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
	"github.com/mokto/terraform-provider-hrobot/internal/client"

	sshx "github.com/mokto/terraform-provider-hrobot/internal/ssh"
)

// buildAutosetupContent generates autosetup configuration from parameters
func buildAutosetupContent(serverName, arch, cryptPassword string) string {
	// Build the autosetup content
	content := fmt.Sprintf(`CRYPTPASSWORD %s
DRIVE1 /dev/nvme0n1
DRIVE2 /dev/nvme1n1
SWRAID 1
SWRAIDLEVEL 0
BOOTLOADER grub
PART /boot/efi esp 512M
PART /boot ext4 1G
PART /     ext4 all crypt
IMAGE /root/images/Ubuntu-2404-noble-%s-base.tar.gz
HOSTNAME %s`, cryptPassword, arch, serverName)

	return content
}

func (r *configurationResource) configure(fp []string, ip string, plan configurationModel, ctx context.Context) (string, string) {
	// 3) Activate Rescue
	tflog.Info(ctx, "activating rescue mode", map[string]interface{}{
		"server_number":         plan.ServerNumber.ValueInt64(),
		"authorized_keys_count": len(fp),
	})

	_, err := r.providerData.Client.ActivateRescue(int(plan.ServerNumber.ValueInt64()), client.RescueParams{
		OS:            "linux",
		AuthorizedFPs: fp,
	})
	if err != nil {
		return "activate rescue failed", err.Error()
	}

	tflog.Info(ctx, "rescue mode activated", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	// 4) Reset into Rescue
	tflog.Info(ctx, "resetting server to rescue mode", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
	})

	if err := r.providerData.Client.Reset(int(plan.ServerNumber.ValueInt64()), "hw"); err != nil {
		return "reset failed", err.Error()
	}

	tflog.Info(ctx, "server reset completed", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
	})

	// 5) Wait for SSH
	waitMin := int64(20)
	tflog.Info(ctx, "waiting for SSH to become available", map[string]interface{}{
		"server_number":   plan.ServerNumber.ValueInt64(),
		"server_ip":       ip,
		"timeout_minutes": waitMin,
	})

	if err := waitTCP(ip+":22", time.Duration(waitMin)*time.Minute); err != nil {
		return "rescue ssh timeout", err.Error()
	}

	tflog.Info(ctx, "SSH is now available", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	// 6) SSH/SFTP upload
	authMethod := "key"
	if len(fp) == 0 {
		authMethod = "password"
	}

	tflog.Info(ctx, "establishing SSH connection", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
		"auth_method":   authMethod,
	})

	var auth sshx.Auth
	if len(fp) > 0 {
		tflog.Info(ctx, "establishing SSH connection with agent")
		auth = sshx.AuthFromAgent()
	} else {
		return "no ssh keys", "At least one rescue_authorized_key_fingerprint is required for SSH access"
	}
	conn, closeFn, err := sshx.Connect(sshx.Conn{Host: ip, User: "root", Timeout: 3 * time.Minute, Auth: auth, InsecureIgnoreHostKey: true})
	if err != nil {
		return "ssh connect", err.Error()
	}
	defer closeFn()

	tflog.Info(ctx, "SSH connection established", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	// Generate autosetup content from parameters
	serverName := plan.ServerName.ValueString()
	arch := plan.Arch.ValueString()
	cryptPassword := plan.CryptPassword.ValueString()

	tflog.Info(ctx, "generating autosetup configuration", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_name":   serverName,
		"arch":          arch,
	})

	autosetupContent := buildAutosetupContent(serverName, arch, cryptPassword)

	tflog.Info(ctx, "uploading autosetup configuration", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"config_size":   len(autosetupContent),
	})

	if err := sshx.Upload(conn, "/root/setup.conf", []byte(autosetupContent), 0600); err != nil {
		return "upload autosetup", err.Error()
	}

	tflog.Info(ctx, "autosetup configuration uploaded", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
	})

	// Generate postinstall script with the correct password
	postinstallContent := strings.ReplaceAll(postinstallScript, "SECRETPASSWORDREPLACEME", cryptPassword)

	tflog.Info(ctx, "uploading postinstall script", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"script_size":   len(postinstallContent),
	})

	if err := sshx.Upload(conn, "/root/post-install.sh", []byte(postinstallContent), 0700); err != nil {
		return "upload post-install", err.Error()
	}

	tflog.Info(ctx, "setting postinstall script permissions", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
	})

	if _, err := sshx.Run(conn, "chmod +x /root/post-install.sh || true"); err != nil {
		tflog.Warn(ctx, "failed to set postinstall script permissions", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"error":         err.Error(),
		})
	}

	// 7) Run installimage and reboot
	tflog.Info(ctx, "starting installimage process", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	// if _, err := sshx.Run(conn, "/root/.oldroot/nfs/install/installimage -a -c /root/setup.conf -x /root/post-install.sh"); err != nil {
	// 	return "installimage failed", err.Error()
	// }

	// tflog.Info(ctx, "all completed, rebooting server", map[string]interface{}{
	// 	"server_number": plan.ServerNumber.ValueInt64(),
	// 	"server_ip":     ip,
	// })

	// _, err = sshx.Run(conn, "reboot || systemctl reboot || shutdown -r now || true")
	// if err != nil {
	// 	tflog.Warn(ctx, "failed to issue reboot command", map[string]interface{}{
	// 		"server_number": plan.ServerNumber.ValueInt64(),
	// 		"error":         err.Error(),
	// 	})
	// }

	// // 8) Wait for OS SSH to come back
	// tflog.Info(ctx, "waiting for OS to boot after installation", map[string]interface{}{
	// 	"server_number":   plan.ServerNumber.ValueInt64(),
	// 	"server_ip":       ip,
	// 	"timeout_minutes": waitMin,
	// })

	// if err := waitTCP(ip+":22", time.Duration(waitMin)*time.Minute); err != nil {
	// 	tflog.Warn(ctx, "initial OS boot timeout, retrying with extended timeout", map[string]interface{}{
	// 		"server_number": plan.ServerNumber.ValueInt64(),
	// 		"server_ip":     ip,
	// 		"error":         err.Error(),
	// 	})

	// 	// give a little more
	// 	if err2 := waitTCP(ip+":22", 15*time.Minute); err2 != nil {
	// 		return "os ssh timeout", fmt.Sprintf("%v / %v", err, err2)
	// 	}
	// }

	// tflog.Info(ctx, "OS is now available via SSH", map[string]interface{}{
	// 	"server_number": plan.ServerNumber.ValueInt64(),
	// 	"server_ip":     ip,
	// })

	// tflog.Info(ctx, "configuration finished", map[string]interface{}{
	// 	"server_number": plan.ServerNumber.ValueInt64(),
	// 	"server_name":   plan.ServerName.ValueString(),
	// 	"ip":            plan.ServerIP.ValueString(),
	// })

	return "", ""
}
