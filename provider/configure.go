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
func buildAutosetupContent(serverName, arch, cryptPassword string, raidLevel int64, drive1, drive2 string) string {
	// Build the autosetup content
	content := fmt.Sprintf(`CRYPTPASSWORD %s
DRIVE1 %s
DRIVE2 %s
SWRAID 1
SWRAIDLEVEL %d
BOOTLOADER grub
PART /boot/efi esp 512M
PART /boot ext4 1G
PART /     ext4 all crypt
IMAGE /root/images/Ubuntu-2404-noble-%s-base.tar.gz
HOSTNAME %s`, cryptPassword, drive1, drive2, raidLevel, arch, serverName)

	return content
}

func (r *configurationResource) configure(fp []string, ip string, plan configurationModel, ctx context.Context) (string, string) {

	summary, error := r.preInstall(fp, ip, plan, ctx)
	if error != "" {
		return summary, error
	}

	summary, error = r.postInstallFirstRun(fp, ip, plan, ctx)
	if error != "" {
		return summary, error
	}

	tflog.Info(ctx, "configuration finished", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_name":   plan.ServerName.ValueString(),
		"ip":            plan.ServerIP.ValueString(),
	})

	return "", ""
}

func (r *configurationResource) preInstall(fp []string, ip string, plan configurationModel, ctx context.Context) (string, string) {

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
	tflog.Info(ctx, "establishing SSH connection", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
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

	// Detect available disks
	tflog.Info(ctx, "detecting available disks", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
	})

	diskOutput, err := sshx.Run(conn, "lsblk -d -o NAME,SIZE,TYPE | grep disk")
	if err != nil {
		return "disk detection failed", fmt.Sprintf("Failed to detect disks: %v", err)
	}

	// Parse disk output to get exactly 2 disks
	diskLines := strings.Split(strings.TrimSpace(diskOutput), "\n")
	if len(diskLines) != 2 {
		return "invalid disk count", fmt.Sprintf("Expected exactly 2 disks, found %d disks: %s", len(diskLines), diskOutput)
	}

	// Extract disk names (first field of each line)
	var drive1, drive2 string
	for i, line := range diskLines {
		fields := strings.Fields(line)
		if len(fields) < 1 {
			return "disk parsing error", fmt.Sprintf("Could not parse disk line: %s", line)
		}
		diskName := "/dev/" + fields[0]
		if i == 0 {
			drive1 = diskName
		} else {
			drive2 = diskName
		}
	}

	tflog.Info(ctx, "detected disks", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"drive1":        drive1,
		"drive2":        drive2,
	})

	// Generate autosetup content from parameters
	serverName := plan.ServerName.ValueString()
	arch := plan.Arch.ValueString()
	cryptPassword := plan.CryptPassword.ValueString()

	// Default raid level to 1 if not specified
	raidLevel := int64(1)
	if !plan.RaidLevel.IsNull() && !plan.RaidLevel.IsUnknown() {
		raidLevel = plan.RaidLevel.ValueInt64()
	}

	tflog.Info(ctx, "generating autosetup configuration", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_name":   serverName,
		"arch":          arch,
		"raid_level":    raidLevel,
	})

	autosetupContent := buildAutosetupContent(serverName, arch, cryptPassword, raidLevel, drive1, drive2)

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

	// Generate postinstall script with the correct password and local IP
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

	if _, err := sshx.Run(conn, "/root/.oldroot/nfs/install/installimage -a -c /root/setup.conf -x /root/post-install.sh"); err != nil {
		return "installimage failed", err.Error()
	}

	tflog.Info(ctx, "all completed, rebooting server", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	_, err = sshx.Run(conn, "reboot || systemctl reboot || shutdown -r now || true")
	if err != nil {
		tflog.Warn(ctx, "failed to issue reboot command", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"error":         err.Error(),
		})
	}

	// 8) Wait for OS SSH to come back
	tflog.Info(ctx, "waiting for OS to boot after installation", map[string]interface{}{
		"server_number":   plan.ServerNumber.ValueInt64(),
		"server_ip":       ip,
		"timeout_minutes": waitMin,
	})

	time.Sleep(10 * time.Second)
	if err := waitTCP(ip+":22", time.Duration(waitMin)*time.Minute); err != nil {
		tflog.Warn(ctx, "initial OS boot timeout, retrying with extended timeout", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"server_ip":     ip,
			"error":         err.Error(),
		})

		// give a little more
		if err2 := waitTCP(ip+":22", 15*time.Minute); err2 != nil {
			return "os ssh timeout", fmt.Sprintf("%v / %v", err, err2)
		}
	}

	tflog.Info(ctx, "OS is now available via SSH", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	return "", ""
}

func (r *configurationResource) postInstallFirstRun(fp []string, ip string, plan configurationModel, ctx context.Context) (string, string) {

	tflog.Info(ctx, "establishing SSH connection", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	var auth sshx.Auth
	if len(fp) > 0 {
		tflog.Info(ctx, "establishing SSH connection with agent")
		auth = sshx.AuthFromAgent()
	} else {
		return "no ssh keys", "At least one rescue_authorized_key_fingerprint is required for SSH access"
	}
	conn, closeFn2, err := sshx.Connect(sshx.Conn{Host: ip, User: "root", Timeout: 3 * time.Minute, Auth: auth, InsecureIgnoreHostKey: true})
	if err != nil {
		return "ssh connect", err.Error()
	}
	defer closeFn2()

	tflog.Info(ctx, "SSH connection established", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	// Add local IP configuration if provided
	localIP := ""
	if !plan.LocalIP.IsNull() && !plan.LocalIP.IsUnknown() {
		localIP = plan.LocalIP.ValueString()
	}

	// Add extra script commands if provided
	extraScript := ""
	if !plan.ExtraScript.IsNull() && !plan.ExtraScript.IsUnknown() {
		extraScript = plan.ExtraScript.ValueString()
	}

	postinstallFirstRunContent := strings.ReplaceAll(postinstallFirstRunScript, "LOCALIPADDRESSREPLACEME", localIP)
	postinstallFirstRunContent = strings.ReplaceAll(postinstallFirstRunContent, "# EXTRASCRIPTREPLACEME", extraScript)

	tflog.Info(ctx, "uploading postinstall - first run script", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"script_size":   len(postinstallFirstRunContent),
	})

	if err := sshx.Upload(conn, "/root/initialize.sh", []byte(postinstallFirstRunContent), 0700); err != nil {
		return "upload initialize", err.Error()
	}

	if _, err := sshx.Run(conn, "chmod +x /root/initialize.sh && /root/initialize.sh"); err != nil {
		tflog.Warn(ctx, "failed to run script permissions", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"error":         err.Error(),
		})
	}

	return "", ""
}
