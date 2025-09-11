package provider

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
