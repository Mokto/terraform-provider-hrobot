package provider

import (
	"context"
	"fmt"
	"strings"
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
	Description  types.String `tfsdk:"description"`
	VSwitchID    types.Int64  `tfsdk:"vswitch_id"`

	// Autosetup parameters
	Arch          types.String `tfsdk:"arch"`
	CryptPassword types.String `tfsdk:"cryptpassword"`

	RescueKeyFPs types.List `tfsdk:"rescue_authorized_key_fingerprints"`
}

func NewResourceConfiguration() resource.Resource { return &configurationResource{} }

// postinstallScript is the comprehensive post-install script for LUKS encryption setup
const postinstallScript = `#!/bin/bash
# Post-install (rescue mode) - auto-unlock for first reboot (v5, fixed cryptroot conf)
set -euo pipefail

CRYPT_PASSWORD="SECRETPASSWORDREPLACEME"
TARGET_MOUNT="${TARGET_MOUNT:-/mnt}"
DISK="${DISK:-/dev/sda}"
LOG="/root/postinstall-autounlock.log"

log() { echo "[INFO] $*" | tee -a "$LOG"; }
die() { echo "[ERROR] $*" | tee -a "$LOG" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "Missing required tool: $1"; }
need cryptsetup; need lsblk; need blkid; need awk; need sed; need findmnt

base_disk() {
  # Given /dev/sda3 -> /dev/sda ; /dev/nvme0n1p3 -> /dev/nvme0n1
  local dev="$1"
  if [[ "$dev" =~ ^/dev/nvme[0-9]+n[0-9]+p[0-9]+$ ]]; then
    echo "${dev%p*}"
  else
    echo "${dev%%[0-9]*}"
  fi
}

find_luks_mapper() {
  # Find any opened LUKS device mapper
  for mapper in /dev/mapper/*; do
    [ -e "$mapper" ] || continue
    [ "$mapper" = "/dev/mapper/control" ] && continue

    # Check if this is a LUKS device
    if cryptsetup status "$(basename "$mapper")" 2>/dev/null | grep -q "type:.*LUKS"; then
      echo "$mapper"
      return 0
    fi
  done
  return 1
}

pick_luks_partition() {
  # Find LUKS partitions
  local -a cand=()

  # Method 1: blkid
  while IFS= read -r dev; do
    [ -n "$dev" ] && cand+=("$dev")
  done < <(blkid -t TYPE=crypto_LUKS -o device 2>/dev/null || true)

  # Method 2: cryptsetup isLuks
  while read -r dev type; do
    [[ "$type" == "part" ]] || continue
    if cryptsetup isLuks -q "$dev" 2>/dev/null; then
      local already_found=0
      for existing in "${cand[@]}"; do
        [ "$existing" = "$dev" ] && already_found=1 && break
      done
      [ $already_found -eq 0 ] && cand+=("$dev")
    fi
  done < <(lsblk -rpo NAME,TYPE)

  # Check if we found any LUKS partitions
  if [ ${#cand[@]} -eq 0 ]; then
    log "No LUKS partitions found"
    return 1
  fi

  log "Found ${#cand[@]} LUKS partition(s): ${cand[*]}"

  # If only one, use it
  if [ ${#cand[@]} -eq 1 ]; then
    echo "${cand[0]}"
    return 0
  fi

  # Multiple partitions: prefer same base disk as $DISK
  local target_base
  target_base="$(base_disk "$DISK")"

  for d in "${cand[@]}"; do
    local b
    b="$(base_disk "$d")"
    if [[ "$b" == "$target_base" ]]; then
      echo "$d"
      return 0
    fi
  done

  # Fallback to first found
  echo "${cand[0]}"
}

ensure_mounted_target() {
  if mountpoint -q "$TARGET_MOUNT"; then
    log "Target already mounted at $TARGET_MOUNT"
    return 0
  fi

  # Check if any LUKS mapper device exists
  local existing_mapper
  if existing_mapper=$(find_luks_mapper); then
    log "Found existing LUKS mapper: $existing_mapper"
    mkdir -p "$TARGET_MOUNT"
    mount "$existing_mapper" "$TARGET_MOUNT" 2>/dev/null || true
  fi

  if ! mountpoint -q "$TARGET_MOUNT"; then
    log "Target not mounted; discovering and opening LUKS root..."
    local luks_part
    if ! luks_part="$(pick_luks_partition)"; then
      die "No LUKS partition found on this system."
    fi
    log "Selected LUKS partition: $luks_part"

    # Generate mapper name based on what's in use or use cryptroot
    local mapper_name="cryptroot"

    log "Opening LUKS device as $mapper_name..."
    echo -n "$CRYPT_PASSWORD" | cryptsetup open "$luks_part" "$mapper_name" --key-file=-

    mkdir -p "$TARGET_MOUNT"
    mount "/dev/mapper/$mapper_name" "$TARGET_MOUNT"
  fi

  # Mount /boot and /boot/efi from same base disk as $DISK
  local target_base
  target_base="$(base_disk "$DISK")"
  mkdir -p "$TARGET_MOUNT/boot" "$TARGET_MOUNT/boot/efi"

  # /boot: ext4 partition on target_base (likely /dev/sda2)
  if ! mountpoint -q "$TARGET_MOUNT/boot"; then
    # For standard Ubuntu encrypted installs, /boot is usually the ext4 partition
    local boot
    boot=$(lsblk -rpo NAME,TYPE,FSTYPE | awk -v b="$target_base" '$2=="part" && $1 ~ "^"b && $3=="ext4"{print $1}' | head -n1 || true)
    if [[ -n "$boot" ]]; then
      log "Mounting boot partition: $boot"
      mount "$boot" "$TARGET_MOUNT/boot" || true
    fi
  fi

  # EFI: vfat partition (ESP) on target_base
  if ! mountpoint -q "$TARGET_MOUNT/boot/efi"; then
    local efi
    efi=$(lsblk -rpo NAME,TYPE,FSTYPE | awk -v b="$target_base" '$2=="part" && $1 ~ "^"b && $3=="vfat"{print $1}' | head -n1 || true)
    if [[ -n "$efi" ]]; then
      log "Mounting EFI partition: $efi"
      mount "$efi" "$TARGET_MOUNT/boot/efi" || true
    fi
  fi
}

# Main execution
ensure_mounted_target

[ -f "$TARGET_MOUNT/etc/os-release" ] || die "No OS found at $TARGET_MOUNT (missing etc/os-release)."

# Detect root mapper and underlying LUKS device
ROOT_DEV=$(findmnt -no SOURCE "$TARGET_MOUNT")
log "Root device: $ROOT_DEV"

# Get the mapper name
MAPPER_NAME=$(basename "$ROOT_DEV")
log "Mapper name: $MAPPER_NAME"

# Find underlying LUKS device
UNDERLYING=""
# Try using cryptsetup status first
UNDERLYING=$(cryptsetup status "$MAPPER_NAME" 2>/dev/null | awk '/device:/{print $2}')

# Fallback: find the LUKS partition directly
if [ -z "$UNDERLYING" ] || [ ! -e "$UNDERLYING" ]; then
  UNDERLYING=$(pick_luks_partition)
fi

# Verify it's a LUKS device
if ! cryptsetup isLuks "$UNDERLYING" 2>/dev/null; then
  die "$UNDERLYING is not a LUKS device"
fi

LUKS_UUID=$(blkid -s UUID -o value "$UNDERLYING" || true)
[ -n "$LUKS_UUID" ] || die "Could not read LUKS UUID from $UNDERLYING."

log "Root mapper: $ROOT_DEV (LUKS on $UNDERLYING, UUID=$LUKS_UUID)"

# Create keyfile
KEYFILE_PATH="/crypto_keyfile.bin"
KEYFILE_ABS="$TARGET_MOUNT$KEYFILE_PATH"

log "Creating keyfile at $KEYFILE_ABS..."
dd if=/dev/urandom of="$KEYFILE_ABS" bs=4096 count=1 status=none
chmod 0400 "$KEYFILE_ABS"
chown root:root "$KEYFILE_ABS"

log "Adding keyfile to LUKS keyslots..."
echo -n "$CRYPT_PASSWORD" | cryptsetup luksAddKey "$UNDERLYING" "$KEYFILE_ABS" --key-file=- || {
  log "Warning: Failed to add keyfile (may already exist in keyslot)"
}

# Update /etc/crypttab - this is crucial for initramfs generation
CRYPTTAB="$TARGET_MOUNT/etc/crypttab"
log "Configuring $CRYPTTAB..."

# Use the simplest format that works reliably
cat > "$CRYPTTAB" << EOF
cryptroot UUID=${LUKS_UUID} /crypto_keyfile.bin luks
EOF

log "Updated /etc/crypttab:"
cat "$CRYPTTAB" | sed 's/^/  /'

# Bind mounts for chroot
for d in proc sys dev run; do
  if ! mountpoint -q "$TARGET_MOUNT/$d"; then
    log "Bind mounting /$d"
    mount --bind "/$d" "$TARGET_MOUNT/$d"
  fi
done

# Install and configure cryptsetup-initramfs
log "Installing cryptsetup-initramfs..."
chroot "$TARGET_MOUNT" bash -c "
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq cryptsetup cryptsetup-initramfs cryptsetup-bin cloud-init
"

# Configure initramfs to include the keyfile
log "Configuring initramfs to include keyfile..."

# Method 1: Configure cryptsetup-initramfs to include the keyfile
mkdir -p "$TARGET_MOUNT/etc/cryptsetup-initramfs"
cat > "$TARGET_MOUNT/etc/cryptsetup-initramfs/conf-hook" << 'EOF'
# Cryptsetup initramfs configuration
KEYFILE_PATTERN="/crypto_keyfile.bin"
UMASK=0077
EOF

# Method 2: Create a custom hook to ensure keyfile is copied
log "Creating initramfs hook for keyfile..."
cat > "$TARGET_MOUNT/etc/initramfs-tools/hooks/crypto_keyfile" << 'EOF'
#!/bin/sh
# Hook to include crypto keyfile in initramfs

PREREQ="cryptroot"

prereqs() {
    echo "$PREREQ"
}

case $1 in
    prereqs)
        prereqs
        exit 0
        ;;
esac

. /usr/share/initramfs-tools/hook-functions

# Copy the keyfile to initramfs root
if [ -e "/crypto_keyfile.bin" ]; then
    echo "Including /crypto_keyfile.bin in initramfs"
    cp /crypto_keyfile.bin "${DESTDIR}/crypto_keyfile.bin"
    chmod 0400 "${DESTDIR}/crypto_keyfile.bin"

    # Also copy to cryptroot directory if it exists
    if [ -d "${DESTDIR}/cryptroot" ]; then
        cp /crypto_keyfile.bin "${DESTDIR}/cryptroot/keyfile"
        chmod 0400 "${DESTDIR}/cryptroot/keyfile"
    fi
fi

exit 0
EOF

chmod +x "$TARGET_MOUNT/etc/initramfs-tools/hooks/crypto_keyfile"

# Method 3: Create a keyscript that will use the keyfile
log "Creating keyscript for keyfile..."
mkdir -p "$TARGET_MOUNT/lib/cryptsetup/scripts"
cat > "$TARGET_MOUNT/lib/cryptsetup/scripts/unlock_keyfile" << 'EOF'
#!/bin/sh
# Simple keyscript to output the keyfile content

if [ -e "/crypto_keyfile.bin" ]; then
    cat /crypto_keyfile.bin
else
    # Fallback to password prompt
    /lib/cryptsetup/askpass "Enter passphrase: "
fi
EOF
chmod +x "$TARGET_MOUNT/lib/cryptsetup/scripts/unlock_keyfile"

# Method 4: Manually create the cryptroot configuration for initramfs
log "Creating manual cryptroot configuration..."
mkdir -p "$TARGET_MOUNT/etc/initramfs-tools/conf.d"

# Create the cryptroot configuration that will be included in initramfs
cat > "$TARGET_MOUNT/etc/initramfs-tools/conf.d/cryptroot" << EOF
# Cryptroot configuration for initramfs
export CRYPTROOT="target=cryptroot,source=UUID=${LUKS_UUID},key=/crypto_keyfile.bin,rootdev"
EOF

# Also create a script to ensure crypttab processing
cat > "$TARGET_MOUNT/etc/initramfs-tools/scripts/local-top/cryptroot-keyfile" << 'EOF'
#!/bin/sh

PREREQ="cryptroot-prepare"

prereqs() {
    echo "$PREREQ"
}

case $1 in
    prereqs)
        prereqs
        exit 0
        ;;
esac

# If keyfile exists, try to unlock with it
if [ -e "/crypto_keyfile.bin" ] && [ -e "/dev/disk/by-uuid/dc4b981c-be0f-449c-8eea-c9d7d1745e9e" ]; then
    echo "Attempting to unlock with keyfile..."
    cryptsetup open /dev/disk/by-uuid/dc4b981c-be0f-449c-8eea-c9d7d1745e9e cryptroot --key-file=/crypto_keyfile.bin 2>/dev/null && exit 0
fi

exit 0
EOF

# Make the script executable and update the UUID
sed -i "s/dc4b981c-be0f-449c-8eea-c9d7d1745e9e/${LUKS_UUID}/g" "$TARGET_MOUNT/etc/initramfs-tools/scripts/local-top/cryptroot-keyfile"
chmod +x "$TARGET_MOUNT/etc/initramfs-tools/scripts/local-top/cryptroot-keyfile"

# Configure kernel command line for cryptroot
log "Configuring kernel parameters..."
# Ensure cryptopts is set correctly
if ! grep -q "GRUB_CMDLINE_LINUX=.*cryptopts=" "$TARGET_MOUNT/etc/default/grub"; then
    sed -i 's/^GRUB_CMDLINE_LINUX="\(.*\)"/GRUB_CMDLINE_LINUX="\1 cryptopts=target=cryptroot,source=UUID='${LUKS_UUID}',key=\/crypto_keyfile.bin"/' "$TARGET_MOUNT/etc/default/grub"
fi

# Make sure the root device is set correctly
sed -i 's|^GRUB_CMDLINE_LINUX_DEFAULT="\(.*\)"|GRUB_CMDLINE_LINUX_DEFAULT="\1 root=/dev/mapper/cryptroot"|' "$TARGET_MOUNT/etc/default/grub"

# Update initramfs with all our changes
log "Updating initramfs (this may take a moment)..."
chroot "$TARGET_MOUNT" bash -c "update-initramfs -c -k all"

# Verify keyfile was included
log "Verifying keyfile in initramfs..."
chroot "$TARGET_MOUNT" bash -c "
  INITRD=\$(ls /boot/initrd.img-* | head -n1)
  echo 'Checking for keyfile in initramfs...'
  if lsinitramfs \"\$INITRD\" | grep -q crypto_keyfile.bin; then
    echo '✓ Keyfile found in initramfs'
  else
    echo '✗ WARNING: Keyfile may not be in initramfs'
  fi

  echo 'Checking for cryptroot configuration...'
  if lsinitramfs \"\$INITRD\" 2>/dev/null | grep -q 'conf/conf.d/cryptroot'; then
    echo '✓ Cryptroot configuration found'
  else
    echo '✗ WARNING: Cryptroot configuration may be missing'
    # Try to manually create it
    echo 'Attempting manual configuration...'
    mkdir -p /tmp/initramfs-fix
    cd /tmp/initramfs-fix
    unmkinitramfs \"\$INITRD\" . 2>/dev/null || true

    # Check if we need to add the configuration
    if [ -d main ]; then
      mkdir -p main/conf/conf.d
      echo 'target=cryptroot,source=UUID=${LUKS_UUID},key=/crypto_keyfile.bin' > main/conf/conf.d/cryptroot

      # Repack initramfs
      find . | cpio -H newc -o 2>/dev/null | gzip > \"\$INITRD.new\"
      mv \"\$INITRD.new\" \"\$INITRD\"
      echo 'Manual configuration added'
    fi
    cd /
    rm -rf /tmp/initramfs-fix
  fi
"

# Update GRUB
log "Installing GRUB to $DISK..."
chroot "$TARGET_MOUNT" bash -c "grub-install '$DISK'"
log "Updating GRUB configuration..."
chroot "$TARGET_MOUNT" bash -c "update-grub"
`

// buildAutosetupContent generates autosetup configuration from parameters
func buildAutosetupContent(serverName, arch, cryptPassword string) string {
	// Build the autosetup content
	content := fmt.Sprintf(`CRYPTPASSWORD %s
DRIVE1 /dev/sda
BOOTLOADER grub
PART /boot/efi esp 512M
PART /boot ext4 1G
PART /     ext4 all crypt
IMAGE /root/images/Ubuntu-2404-noble-%s-base.tar.gz
HOSTNAME %s`, cryptPassword, arch, serverName)

	return content
}

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

	fp := mustStringSlice(ctx, resp, plan.RescueKeyFPs)
	if resp.Diagnostics.HasError() {
		return
	}

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

	// 3) Activate Rescue
	tflog.Info(ctx, "activating rescue mode", map[string]interface{}{
		"server_number":         plan.ServerNumber.ValueInt64(),
		"authorized_keys_count": len(fp),
	})

	rescue, err := r.providerData.Client.ActivateRescue(int(plan.ServerNumber.ValueInt64()), client.RescueParams{
		OS:            "linux",
		AuthorizedFPs: fp,
	})
	if err != nil {
		resp.Diagnostics.AddError("activate rescue failed", err.Error())
		return
	}
	ip := rescue.ServerIP

	tflog.Info(ctx, "rescue mode activated", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	// 4) Reset into Rescue
	tflog.Info(ctx, "resetting server to rescue mode", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
	})

	if err := r.providerData.Client.Reset(int(plan.ServerNumber.ValueInt64()), "hw"); err != nil {
		resp.Diagnostics.AddError("reset failed", err.Error())
		return
	}

	tflog.Info(ctx, "server reset completed", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
	})

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

	// 5) Wait for SSH
	waitMin := int64(20)
	tflog.Info(ctx, "waiting for SSH to become available", map[string]interface{}{
		"server_number":   plan.ServerNumber.ValueInt64(),
		"server_ip":       ip,
		"timeout_minutes": waitMin,
	})

	if err := waitTCP(ip+":22", time.Duration(waitMin)*time.Minute); err != nil {
		resp.Diagnostics.AddError("rescue ssh timeout", err.Error())
		return
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
		tflog.Info(ctx, "establishing SSH connection with password")
		auth = sshx.AuthPassword(rescue.Password)
	}
	conn, closeFn, err := sshx.Connect(sshx.Conn{Host: ip, User: "root", Timeout: 3 * time.Minute, Auth: auth, InsecureIgnoreHostKey: true})
	if err != nil {
		resp.Diagnostics.AddError("ssh connect", err.Error())
		return
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

	if err := sshx.Upload(conn, "/autosetup", []byte(autosetupContent), 0600); err != nil {
		resp.Diagnostics.AddError("upload autosetup", err.Error())
		return
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
		resp.Diagnostics.AddError("upload post-install", err.Error())
		return
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

	if _, err := sshx.Run(conn, "installimage -a /autosetup"); err != nil {
		resp.Diagnostics.AddError("installimage failed", err.Error())
		return
	}

	tflog.Info(ctx, "installimage completed, rebooting server", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	_, _ = sshx.Run(conn, "reboot || systemctl reboot || shutdown -r now || true")

	// 8) Wait for OS SSH to come back
	tflog.Info(ctx, "waiting for OS to boot after installation", map[string]interface{}{
		"server_number":   plan.ServerNumber.ValueInt64(),
		"server_ip":       ip,
		"timeout_minutes": waitMin,
	})

	if err := waitTCP(ip+":22", time.Duration(waitMin)*time.Minute); err != nil {
		tflog.Warn(ctx, "initial OS boot timeout, retrying with extended timeout", map[string]interface{}{
			"server_number": plan.ServerNumber.ValueInt64(),
			"server_ip":     ip,
			"error":         err.Error(),
		})

		// give a little more
		if err2 := waitTCP(ip+":22", 15*time.Minute); err2 != nil {
			resp.Diagnostics.AddError("os ssh timeout", fmt.Sprintf("%v / %v", err, err2))
			return
		}
	}

	tflog.Info(ctx, "OS is now available via SSH", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_ip":     ip,
	})

	state := plan
	state.ID = types.StringValue(fmt.Sprintf("configuration-%d", time.Now().Unix()))
	// ServerIP is already set from the plan since it's now required

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	tflog.Info(ctx, "configuration finished", map[string]interface{}{
		"server_number": plan.ServerNumber.ValueInt64(),
		"server_name":   plan.ServerName.ValueString(),
		"ip":            plan.ServerIP.ValueString(),
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
