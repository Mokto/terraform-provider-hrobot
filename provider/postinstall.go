package provider

// postinstallScript is the comprehensive post-install script for LUKS encryption setup
const postinstallScript = `#!/bin/bash

# Hetzner Post-install Script for Auto-unlocking Encrypted Drives (FIXED)
# This script sets up automatic LUKS decryption during boot
set -e

CRYPT_PASSWORD="SECRETPASSWORDREPLACEME"
LUKS_DEVICE="/dev/md2"  # Fixed: md2 is the encrypted device
KEYFILE_PATH="/etc/luks-keys/boot.key"
KEYFILE_DIR="/etc/luks-keys"

echo "Starting Hetzner auto-unlock setup..."

# Create directory for key files
mkdir -p "$KEYFILE_DIR"
chmod 700 "$KEYFILE_DIR"

# Generate a random key for automatic unlocking
dd if=/dev/urandom of="$KEYFILE_PATH" bs=512 count=1
chmod 600 "$KEYFILE_PATH"

# Add the key to the LUKS device (with proper error handling and debugging)
echo "Adding key file to LUKS device $LUKS_DEVICE..."

# Test the password first
echo "Testing password against LUKS device..."
if ! echo "$CRYPT_PASSWORD" | cryptsetup luksOpen --test-passphrase "$LUKS_DEVICE"; then
    echo "ERROR: Password test failed"
    echo "This could be due to:"
    echo "1. Incorrect password"
    echo "2. LUKS device not properly initialized"
    echo "3. Device busy or mounted"

    # Additional debugging
    echo ""
    echo "LUKS device info:"
    cryptsetup luksDump "$LUKS_DEVICE" 2>&1 || echo "Failed to dump LUKS info"

    exit 1
fi

echo "Password test successful, adding key file..."

# Small delay to ensure device state is stable
sleep 1

# Show current keyslots before adding
echo "Current keyslots before addition:"
cryptsetup luksDump "$LUKS_DEVICE" | grep -A10 "Keyslots:" || echo "Failed to show keyslots"

# Add the key with more verbose output and alternative method
echo "Attempting to add key file..."

# Method 1: Try with stdin
if echo "$CRYPT_PASSWORD" | cryptsetup luksAddKey "$LUKS_DEVICE" "$KEYFILE_PATH" --key-file=- --verbose; then
    echo "Key file successfully added to LUKS device (method 1)"
    KEY_ADDED=true
else
    echo "Method 1 failed, trying method 2..."

    # Method 2: Try with temporary file
    TEMP_PASS_FILE=$(mktemp)
    echo "$CRYPT_PASSWORD" > "$TEMP_PASS_FILE"
    chmod 600 "$TEMP_PASS_FILE"

    if cryptsetup luksAddKey "$LUKS_DEVICE" "$KEYFILE_PATH" --key-file="$TEMP_PASS_FILE" --verbose; then
        echo "Key file successfully added to LUKS device (method 2)"
        KEY_ADDED=true
    else
        echo "Method 2 also failed, trying method 3..."

        # Method 3: Interactive mode (shouldn't work in automated script, but let's try)
        if cryptsetup luksAddKey "$LUKS_DEVICE" "$KEYFILE_PATH" --verbose < "$TEMP_PASS_FILE"; then
            echo "Key file successfully added to LUKS device (method 3)"
            KEY_ADDED=true
        else
            KEY_ADDED=false
        fi
    fi

    # Clean up temp file
    rm -f "$TEMP_PASS_FILE"
fi

if [ "$KEY_ADDED" != "true" ]; then
    RESULT=$?
    echo "ERROR: All methods failed to add key file to LUKS device"

    # Additional debugging
    echo ""
    echo "Final LUKS keyslots:"
    cryptsetup luksDump "$LUKS_DEVICE" | grep -A10 "Keyslots:" || echo "Failed to show keyslots"

    echo ""
    echo "Key file info:"
    ls -la "$KEYFILE_PATH"
    hexdump -C "$KEYFILE_PATH" | head -2

    echo ""
    echo "Device status:"
    lsblk | grep md2

    exit 1
fi

# Verify the key file works
echo "Testing key file..."
if cryptsetup luksOpen --test-passphrase --key-file="$KEYFILE_PATH" "$LUKS_DEVICE"; then
    echo "Key file test successful"
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
mkdir -p /etc/dropbear-initramfs
echo 'DROPBEAR_OPTIONS="-p 2222 -s -j -k -I 60"' > /etc/dropbear-initramfs/config

# Copy SSH keys
if [ -f /root/.ssh/authorized_keys ]; then
    cp /root/.ssh/authorized_keys /etc/dropbear-initramfs/authorized_keys
    chmod 600 /etc/dropbear-initramfs/authorized_keys
    echo "SSH keys copied for dropbear access"
else
    echo "Warning: No SSH keys found at /root/.ssh/authorized_keys"
    echo "Dropbear configured but no SSH keys available for remote access"
fi

# Update initramfs again to include dropbear
echo "Updating initramfs to include dropbear..."
update-initramfs -u -k all

echo "Dropbear configured. You can SSH to port 2222 during boot if needed."
echo "Use: ssh -p 2222 root@your-server-ip"
echo "Then run: cryptroot-unlock"

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
