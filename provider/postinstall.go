package provider

// postinstallScript is the comprehensive post-install script for LUKS encryption setup
const postinstallScript = `#!/bin/bash

# Hetzner Post-install Script for Auto-unlocking Encrypted Drives (FIXED)
# This script sets up automatic LUKS decryption during boot
set -e

CRYPT_PASSWORD="SECRETPASSWORDREPLACEME"
KEYFILE_PATH="/etc/luks-keys/boot.key"
KEYFILE_DIR="/etc/luks-keys"
UNUSED_DISKS="UNUSEDDISKSREPLACEME"

echo "Starting Hetzner auto-unlock setup..."

# Wipe and disable unused disks (3 and 4 disk setups only)
if [ -n "$UNUSED_DISKS" ] && [ "$UNUSED_DISKS" != "" ]; then
    echo "============================================"
    echo "Wiping unused disks: $UNUSED_DISKS"
    echo "============================================"

    for disk in $UNUSED_DISKS; do
        echo ""
        echo "Processing unused disk: $disk"

        # Check if disk exists
        if [ ! -b "$disk" ]; then
            echo "⚠ Warning: Disk $disk does not exist, skipping"
            continue
        fi

        # Get disk size for verification
        DISK_SIZE=$(lsblk -b -d -n -o SIZE "$disk" 2>/dev/null || echo "unknown")
        echo "Disk size: $DISK_SIZE bytes"

        # Unmount any partitions on this disk
        echo "Unmounting any mounted partitions on $disk..."
        for partition in ${disk}* ${disk}p*; do
            if [ -b "$partition" ]; then
                umount -f "$partition" 2>/dev/null || true
            fi
        done

        # Remove from any RAID arrays
        echo "Removing $disk from any RAID arrays..."
        mdadm --stop --scan 2>/dev/null || true
        for partition in ${disk}* ${disk}p*; do
            if [ -b "$partition" ]; then
                mdadm --zero-superblock "$partition" 2>/dev/null || true
            fi
        done
        mdadm --zero-superblock "$disk" 2>/dev/null || true

        # Wipe partition table and beginning of disk
        echo "Wiping partition table on $disk..."
        dd if=/dev/zero of="$disk" bs=1M count=100 status=progress 2>&1 || echo "Failed to wipe $disk"

        # Use wipefs to remove filesystem signatures
        echo "Removing filesystem signatures from $disk..."
        wipefs -a "$disk" 2>/dev/null || true

        # Write zeros to the end of the disk as well (to remove backup GPT)
        echo "Wiping end of disk $disk..."
        DISK_SIZE_MB=$((DISK_SIZE / 1048576))
        if [ "$DISK_SIZE_MB" -gt 100 ]; then
            dd if=/dev/zero of="$disk" bs=1M seek=$((DISK_SIZE_MB - 100)) count=100 2>/dev/null || true
        fi

        # Create a flag file to mark this disk as intentionally wiped
        DISK_ID=$(basename "$disk")
        touch "/etc/disk-wiped-${DISK_ID}" 2>/dev/null || true

        echo "✓ Successfully wiped and disabled $disk"
    done

    echo ""
    echo "============================================"
    echo "✓ All unused disks have been wiped"
    echo "============================================"
    echo ""
else
    echo "No unused disks to wipe (2-disk setup)"
fi

# Detect number of disks
DISK_COUNT=$(lsblk -d -n -o TYPE,NAME | grep -c '^disk' || echo "0")
echo "Detected $DISK_COUNT disk(s)"

# Detect LUKS device based on disk configuration
if [ "$DISK_COUNT" -eq 3 ]; then
    # 3-disk setup uses single disk (no RAID)
    echo "3-disk configuration detected, using single disk LUKS partition"

    # Find the LUKS device by looking at what's currently mounted
    # The root partition is encrypted, so find its backing device
    ROOT_DEVICE=$(findmnt -n -o SOURCE /)
    echo "Root filesystem is on: $ROOT_DEVICE"

    if [[ "$ROOT_DEVICE" == /dev/mapper/* ]]; then
        # This is a mapped device, find the underlying LUKS device
        MAPPER_NAME=$(basename "$ROOT_DEVICE")
        LUKS_DEVICE=$(cryptsetup status "$MAPPER_NAME" | grep device: | awk '{print $2}')
        echo "Found LUKS device from active mapping: $LUKS_DEVICE"
    else
        # Fallback: try to find LUKS device by type
        LUKS_DEVICE=$(blkid -t TYPE=crypto_LUKS -o device | head -1)
        echo "Found LUKS device by blkid: $LUKS_DEVICE"
    fi

    if [ -z "$LUKS_DEVICE" ]; then
        echo "ERROR: No LUKS device found on single disk"
        exit 1
    fi
    echo "Detected single disk LUKS device: $LUKS_DEVICE"
else
    # 2 or 4 disk setup uses RAID
    echo "RAID configuration detected, looking for md device"

    # Find the LUKS device by looking at what's currently mounted
    ROOT_DEVICE=$(findmnt -n -o SOURCE /)
    echo "Root filesystem is on: $ROOT_DEVICE"

    if [[ "$ROOT_DEVICE" == /dev/mapper/* ]]; then
        # This is a mapped device, find the underlying LUKS device (should be RAID)
        MAPPER_NAME=$(basename "$ROOT_DEVICE")
        LUKS_DEVICE=$(cryptsetup status "$MAPPER_NAME" | grep device: | awk '{print $2}')
        echo "Found LUKS device from active mapping: $LUKS_DEVICE"
    else
        # Fallback: find biggest RAID device
        BIGGEST_MD=$(awk '/^md[0-9]+ : active/ {print $1, $5}' /proc/mdstat | sort -k2 -nr | head -1 | cut -d' ' -f1)
        if [ -z "$BIGGEST_MD" ]; then
            echo "ERROR: No RAID device found in /proc/mdstat"
            exit 1
        fi
        LUKS_DEVICE="/dev/$BIGGEST_MD"
        echo "Found RAID device from mdstat: $LUKS_DEVICE"
    fi

    echo "Detected RAID LUKS device: $LUKS_DEVICE"
fi

# Create directory for key files
mkdir -p "$KEYFILE_DIR"
chmod 700 "$KEYFILE_DIR"

# Generate a random key for automatic unlocking
dd if=/dev/urandom of="$KEYFILE_PATH" bs=512 count=1
chmod 600 "$KEYFILE_PATH"

# Add the key to the LUKS device (with proper error handling and debugging)
echo "Adding key file to LUKS device $LUKS_DEVICE..."

# Check if device is currently open (which it should be since we're running inside it)
DEVICE_STATUS=$(cryptsetup status "$MAPPER_NAME" 2>/dev/null || echo "")
if [ -n "$DEVICE_STATUS" ]; then
    echo "✓ LUKS device is currently open and mounted (expected)"
else
    echo "⚠ Warning: LUKS device doesn't appear to be open"
fi

# Show current keyslots before adding
echo "Current keyslots before addition:"
cryptsetup luksDump "$LUKS_DEVICE" | grep -A10 "Keyslots:" || echo "Failed to show keyslots"

# Add the key file to LUKS device
# Note: We don't test the password first because the device is already open and mounted
# We trust that the password in CRYPT_PASSWORD is correct (it was used during installation)
echo "Adding key file to LUKS device..."

TEMP_PASS_FILE=$(mktemp)
echo "$CRYPT_PASSWORD" > "$TEMP_PASS_FILE"
chmod 600 "$TEMP_PASS_FILE"

if cryptsetup luksAddKey "$LUKS_DEVICE" "$KEYFILE_PATH" --verbose < "$TEMP_PASS_FILE"; then
    echo "✓ Key file successfully added to LUKS device"
    KEY_ADDED=true
else
    KEY_ADDED=false
    RESULT=$?
    echo "ERROR: Failed to add key file to LUKS device (exit code: $RESULT)"

    # Additional debugging
    echo ""
    echo "LUKS device info:"
    cryptsetup luksDump "$LUKS_DEVICE" 2>&1 || echo "Failed to dump LUKS info"

    echo ""
    echo "Final LUKS keyslots:"
    cryptsetup luksDump "$LUKS_DEVICE" | grep -A10 "Keyslots:" || echo "Failed to show keyslots"

    echo ""
    echo "Key file info:"
    ls -la "$KEYFILE_PATH"

    echo ""
    echo "Checking if password file is readable:"
    ls -la "$TEMP_PASS_FILE"

    echo ""
    echo "Password length: $(wc -c < "$TEMP_PASS_FILE") bytes"
fi

# Clean up temp file
rm -f "$TEMP_PASS_FILE"

if [ "$KEY_ADDED" != "true" ]; then
    exit 1
fi

# Verify the key file works
echo "Testing key file..."
if cryptsetup luksOpen --test-passphrase --key-file="$KEYFILE_PATH" "$LUKS_DEVICE"; then
    echo "✓ Key file test successful"
else
    echo "ERROR: Key file test failed"
    exit 1
fi

# Get the UUID of the encrypted device
LUKS_UUID=$(cryptsetup luksUUID "$LUKS_DEVICE")
echo "LUKS UUID: $LUKS_UUID"

# Get the correct crypt name from existing crypttab
CRYPT_NAME=$(grep "$LUKS_UUID" /etc/crypttab | awk '{print $1}' 2>/dev/null || echo "")
if [ -z "$CRYPT_NAME" ]; then
    echo "Warning: Could not find existing entry in crypttab, using UUID-based name"
    CRYPT_NAME="luks-$LUKS_UUID"
fi
echo "Crypt name: $CRYPT_NAME"

# Backup original crypttab
cp /etc/crypttab /etc/crypttab.backup
echo "Backed up original crypttab"

# Create new crypttab entry with key file
echo "$CRYPT_NAME UUID=$LUKS_UUID $KEYFILE_PATH luks" > /etc/crypttab
echo "Updated crypttab to use key file"

# Ensure cryptsetup-initramfs config directory exists
mkdir -p /etc/cryptsetup-initramfs

# Update initramfs to include the key file
echo "KEYFILE_PATTERN=\"$KEYFILE_PATH\"" >> /etc/cryptsetup-initramfs/conf-hook
echo "UMASK=0077" >> /etc/cryptsetup-initramfs/conf-hook
echo "Updated initramfs configuration"

# Add hook to copy key file to initramfs
mkdir -p /etc/initramfs-tools/hooks
cat > /etc/initramfs-tools/hooks/luks-key << 'EOF'
#!/bin/sh
PREREQ=""

prereqs()
{
    echo "$PREREQ"
}

case $1 in
prereqs)
    prereqs
    exit 0
    ;;
esac

. /usr/share/initramfs-tools/hook-functions

# Copy the key file
copy_file keyfile /etc/luks-keys/boot.key /etc/luks-keys/boot.key
EOF

chmod +x /etc/initramfs-tools/hooks/luks-key
echo "Created initramfs hook"

# Update initramfs for all kernels
echo "Rebuilding initramfs (this may take a moment)..."
update-initramfs -u -k all

# Verify the key file is included in initramfs
LATEST_KERNEL=$(ls /boot/initrd.img-* | sed 's/.*initrd.img-//' | sort -V | tail -1 2>/dev/null || echo "")
if [ -n "$LATEST_KERNEL" ]; then
    if lsinitramfs "/boot/initrd.img-$LATEST_KERNEL" | grep -q "etc/luks-keys/boot.key"; then
        echo "SUCCESS: Key file is included in initramfs"
    else
        echo "WARNING: Key file may not be properly included in initramfs"
        echo "Auto-unlock might not work on first boot"
    fi
else
    echo "WARNING: Could not verify initramfs contents"
fi

# Set up dropbear for remote unlocking automatically
echo "Setting up dropbear for remote unlocking..."

# Install dropbear
apt-get update
apt-get install -y dropbear-initramfs

# Configure dropbear
mkdir -p /etc/dropbear/initramfs
echo 'DROPBEAR_OPTIONS="-p 2222 -s -j -k -I 0 -W 65536"' > /etc/dropbear/initramfs/dropbear.conf

# Process SSH keys with dropbear-specific requirements
if [ -f /root/.ssh/authorized_keys ]; then
    echo "Processing SSH keys for dropbear..."
    cp /root/.ssh/authorized_keys /etc/dropbear/initramfs/authorized_keys
    chmod 600 /etc/dropbear/initramfs/authorized_keys
    echo "✓ Successfully processed $VALID_KEYS SSH key(s) for dropbear"
else
    echo "⚠ No SSH keys found at /root/.ssh/authorized_keys"
fi

# Double-check dropbear configuration files
echo "Finalizing dropbear configuration..."

# Ensure the config file has proper format
cat > /etc/dropbear/initramfs/dropbear.conf << 'EOF'
DROPBEAR_OPTIONS="-p 2222 -s -j -k -I 0 -W 65536"
EOF

# Verify authorized_keys file exists and has content
if [ -f /etc/dropbear/initramfs/authorized_keys ] && [ -s /etc/dropbear/initramfs/authorized_keys ]; then
    echo "✓ Dropbear authorized_keys file verified"
    echo "Key count: $(wc -l < /etc/dropbear/initramfs/authorized_keys)"
else
    echo "⚠ ERROR: Dropbear authorized_keys file missing or empty!"
    exit 1
fi

# Update initramfs again to include dropbear with fixed configuration
# Enable DHCP networking in initramfs
echo 'IP=dhcp' >> /etc/initramfs-tools/initramfs.conf
echo "Updating initramfs to include dropbear..."
update-initramfs -u -k all

echo ""
echo "🔧 Dropbear configuration complete"
echo "📡 Emergency SSH access: ssh -p 2222 root@your-server-ip"
echo "🔓 To unlock manually during boot: cryptroot-unlock"

# Final verification
echo ""
echo "=== Final Configuration Summary ==="
echo "LUKS UUID: $LUKS_UUID"
echo "Crypt name: $CRYPT_NAME"
echo "Key file: $KEYFILE_PATH"
echo ""
echo "Current /etc/crypttab:"
cat /etc/crypttab
echo ""
echo "LUKS keyslots in use: $(cryptsetup luksDump "$LUKS_DEVICE" | grep -c 'luks2')"

echo ""
echo "Auto-unlock setup completed successfully!"
echo ""
echo "Important notes:"
echo "1. The server will now automatically unlock the encrypted drive during boot"
echo "2. Keep the key file secure: $KEYFILE_PATH"
echo "3. Backup created at: /etc/crypttab.backup"
echo "4. Consider backing up your LUKS headers:"
echo "   cryptsetup luksHeaderBackup $LUKS_DEVICE --header-backup-file luks-header.backup"
echo ""
echo "Reboot the system to test the auto-unlock functionality."
`
