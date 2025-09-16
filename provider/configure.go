package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/mokto/terraform-provider-hrobot/internal/client"
	sshx "github.com/mokto/terraform-provider-hrobot/internal/ssh"
)

// buildAutosetupContent generates autosetup configuration from parameters
func buildAutosetupContent(serverName, arch, cryptPassword string, raidLevel int64, drive1, drive2 string, noUEFI bool) string {
	// Build the autosetup content
	var content string
	if noUEFI {
		content = fmt.Sprintf(`CRYPTPASSWORD %s
DRIVE1 %s
DRIVE2 %s
SWRAID 1
SWRAIDLEVEL %d
BOOTLOADER grub
PART /boot ext4 1G
PART /     ext4 all crypt
IMAGE /root/images/Ubuntu-2404-noble-%s-base.tar.gz
HOSTNAME %s`, cryptPassword, drive1, drive2, raidLevel, arch, serverName)
	} else {
		content = fmt.Sprintf(`CRYPTPASSWORD %s
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
	}

	return content
}

// buildK3SScript generates K3S installation script from parameters
func buildK3SScript(plan configurationModel, ctx context.Context) string {
	if plan.K3SToken.IsNull() || plan.K3SToken.IsUnknown() || plan.K3SURL.IsNull() || plan.K3SURL.IsUnknown() {
		tflog.Warn(ctx, "K3S parameters not provided, skipping K3S installation")
		return "echo 'K3S parameters not provided, skipping K3S installation'"
	}

	k3sToken := plan.K3SToken.ValueString()
	k3sURL := plan.K3SURL.ValueString()

	var script strings.Builder
	script.WriteString("echo 'Installing K3S agent...'\n")

	// Build kubelet arguments
	var kubeletArgs []string

	kubeletArgs = append(kubeletArgs, "--kubelet-arg=\"--cloud-provider=external\"")

	// Add node labels
	if !plan.NodeLabels.IsNull() && !plan.NodeLabels.IsUnknown() {
		var nodeLabels []nodeLabelModel
		plan.NodeLabels.ElementsAs(ctx, &nodeLabels, false)
		for _, label := range nodeLabels {
			if !label.Name.IsNull() && !label.Value.IsNull() {
				kubeletArgs = append(kubeletArgs, fmt.Sprintf("--node-label %s=%s", label.Name.ValueString(), label.Value.ValueString()))
			}
		}
	}

	// Add taints
	if !plan.Taints.IsNull() && !plan.Taints.IsUnknown() {
		var taints []types.String
		plan.Taints.ElementsAs(ctx, &taints, false)
		for _, taint := range taints {
			if !taint.IsNull() && !taint.IsUnknown() {
				kubeletArgs = append(kubeletArgs, fmt.Sprintf("--kubelet-arg=register-with-taints=%s", taint.ValueString()))
			}
		}
	}

	// Build the complete K3S installation command
	script.WriteString(fmt.Sprintf("curl -sfL https://get.k3s.io | K3S_URL=\"%s\" K3S_TOKEN=%s \\\n", k3sURL, k3sToken))
	script.WriteString("  sh -s - \\\n")

	// Add all kubelet arguments
	for _, arg := range kubeletArgs {
		script.WriteString(fmt.Sprintf("  %s \\\n", arg))
	}

	// Remove the trailing backslash and newline from the last argument
	scriptStr := script.String()
	if strings.HasSuffix(scriptStr, " \\\n") {
		scriptStr = strings.TrimSuffix(scriptStr, " \\\n") + "\n"
	}

	scriptStr += "echo 'K3S installation completed'\n"

	return scriptStr
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

	waitMin := int64(5)
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

	// Check no_uefi parameter
	noUEFI := false
	if !plan.NoUEFI.IsNull() && !plan.NoUEFI.IsUnknown() {
		noUEFI = plan.NoUEFI.ValueBool()
	}

	autosetupContent := buildAutosetupContent(serverName, arch, cryptPassword, raidLevel, drive1, drive2, noUEFI)

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

	// Build K3S installation script
	k3sScript := buildK3SScript(plan, ctx)

	postinstallFirstRunContent := strings.ReplaceAll(postinstallFirstRunScript, "LOCALIPADDRESSREPLACEME", localIP)
	postinstallFirstRunContent = strings.ReplaceAll(postinstallFirstRunContent, "# EXTRASCRIPTREPLACEME", "")

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

	// Close the current SSH connection before rebooting
	closeFn2()

	// Issue reboot command via SSH (non-blocking)
	tflog.Info(ctx, "initiating server reboot", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	// Quick SSH connection just to issue the reboot command
	rebootConn, rebootCloseFn, err := sshx.Connect(sshx.Conn{Host: ip, User: "root", Timeout: 30 * time.Second, Auth: auth, InsecureIgnoreHostKey: true})
	if err != nil {
		return "reboot ssh connect", err.Error()
	}

	// Send reboot command (this will likely cause the connection to drop)
	_, _ = sshx.Run(rebootConn, "nohup reboot > /dev/null 2>&1 &")
	rebootCloseFn()

	// Wait for system to go down and come back up
	tflog.Info(ctx, "waiting for server to reboot", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	// Wait a bit for the reboot to start
	time.Sleep(10 * time.Second)

	// Wait for SSH port to become available again
	if err := waitTCP(ip+":22", 10*time.Minute); err != nil {
		return "reboot ssh timeout", err.Error()
	}

	tflog.Info(ctx, "server back online after reboot, waiting for network connectivity", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	// Establish new SSH connection for post-reboot tasks
	postRebootConn, postRebootCloseFn, err := sshx.Connect(sshx.Conn{Host: ip, User: "root", Timeout: 3 * time.Minute, Auth: auth, InsecureIgnoreHostKey: true})
	if err != nil {
		return "post-reboot ssh connect", err.Error()
	}
	defer postRebootCloseFn()

	// Wait for ping to 10.0.0.120 to succeed
	pingScript := `
#!/bin/bash
PING_COUNT=0
MAX_PING_ATTEMPTS=60  # 5 minutes max

echo "Waiting for ping to 10.0.0.120 to succeed..."
while ! ping -c 1 -W 2 10.0.0.120 > /dev/null 2>&1; do
    PING_COUNT=$((PING_COUNT + 1))
    if [ $PING_COUNT -ge $MAX_PING_ATTEMPTS ]; then
        echo "Error: Failed to ping 10.0.0.120 after $MAX_PING_ATTEMPTS attempts"
        exit 1
    fi
    echo "Attempt $PING_COUNT/$MAX_PING_ATTEMPTS: Waiting for network connectivity..."
    sleep 5
done
echo "✓ Successfully pinged 10.0.0.120, network is ready"
`

	tflog.Info(ctx, "checking network connectivity to 10.0.0.120", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	if _, err := sshx.Run(postRebootConn, pingScript); err != nil {
		return "ping check failed", err.Error()
	}

	// Now run the K3S installation script
	if k3sScript != "" && !strings.Contains(k3sScript, "skipping K3S installation") {
		tflog.Info(ctx, "installing K3S", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"server_ip":     ip,
		})

		if _, err := sshx.Run(postRebootConn, k3sScript); err != nil {
			return "k3s installation failed", err.Error()
		}

		tflog.Info(ctx, "K3S installation completed successfully", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"server_ip":     ip,
		})
	} else {
		tflog.Info(ctx, "K3S installation skipped", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
		})
	}

	return "", ""
}
