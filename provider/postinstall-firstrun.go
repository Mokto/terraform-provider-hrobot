package provider

// postinstallScript is the comprehensive post-install script for LUKS encryption setup
const postinstallFirstRunScript = `#!/bin/bash

LOCAL_IP="LOCALIPADDRESSREPLACEME"

# Verify unused disks remain wiped and create udev rules to prevent mounting
echo "Checking for wiped disks and creating safeguards..."
WIPED_DISKS=$(ls /etc/disk-wiped-* 2>/dev/null | sed 's|/etc/disk-wiped-||' || echo "")
if [ -n "$WIPED_DISKS" ]; then
    echo "Found wiped disks: $WIPED_DISKS"

    # Create udev rules to prevent automatic mounting of these disks
    mkdir -p /etc/udev/rules.d
    cat > /etc/udev/rules.d/99-block-unused-disks.rules << 'UDEV_EOF'
# Prevent unused disks from being mounted or accessed
# Generated automatically by Hetzner provisioning
UDEV_EOF

    for disk_id in $WIPED_DISKS; do
        # Add udev rule to block the device
        echo "KERNEL==\"${disk_id}\", ENV{UDISKS_IGNORE}=\"1\", ENV{UDISKS_PRESENTATION_HIDE}=\"1\"" >> /etc/udev/rules.d/99-block-unused-disks.rules
        echo "KERNEL==\"${disk_id}[0-9]*\", ENV{UDISKS_IGNORE}=\"1\", ENV{UDISKS_PRESENTATION_HIDE}=\"1\"" >> /etc/udev/rules.d/99-block-unused-disks.rules
        echo "KERNEL==\"${disk_id}p[0-9]*\", ENV{UDISKS_IGNORE}=\"1\", ENV{UDISKS_PRESENTATION_HIDE}=\"1\"" >> /etc/udev/rules.d/99-block-unused-disks.rules

        # Verify the disk is still wiped
        DISK_PATH="/dev/${disk_id}"
        if [ -b "$DISK_PATH" ]; then
            # Check if disk has any partition table
            PARTITIONS=$(lsblk -n "$DISK_PATH" 2>/dev/null | wc -l)
            if [ "$PARTITIONS" -gt 1 ]; then
                echo "⚠ WARNING: Disk $DISK_PATH has partitions, re-wiping..."
                dd if=/dev/zero of="$DISK_PATH" bs=1M count=100 2>/dev/null || true
                wipefs -a "$DISK_PATH" 2>/dev/null || true
            else
                echo "✓ Disk $DISK_PATH remains wiped"
            fi
        fi
    done

    # Reload udev rules
    udevadm control --reload-rules 2>/dev/null || true
    udevadm trigger 2>/dev/null || true

    echo "✓ Created udev rules to prevent unused disks from being mounted"
else
    echo "No wiped disks found (2-disk setup)"
fi

# Configure local IP if provided
if [ -n "$LOCAL_IP" ] && [ "$LOCAL_IP" != "" ]; then
    echo "Configuring local IP address: $LOCAL_IP"

    # Get default interface
    DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -1)
    if [ -z "$DEFAULT_IFACE" ]; then
        echo "Warning: Could not determine default interface"
        DEFAULT_IFACE="eth0"  # fallback
    fi
    echo "Using default interface: $DEFAULT_IFACE"

    # Wait for default interface to be fully up
    echo "Waiting for default interface to be ready..."
    for i in {1..30}; do
        if ip link show "$DEFAULT_IFACE" | grep -q "state UP"; then
            echo "✓ Interface $DEFAULT_IFACE is up"
            break
        fi
        echo "Waiting for $DEFAULT_IFACE to come up... ($i/30)"
        sleep 1
    done

    # Create netplan configuration with optimized settings
    mkdir -p /etc/netplan
    cat > /etc/netplan/50-local-ip.yaml << EOF
network:
  version: 2
  ethernets:
    ${DEFAULT_IFACE}:
      mtu: 1500
      optional: false
  vlans:
    ${DEFAULT_IFACE}.4001:
      id: 4001
      link: ${DEFAULT_IFACE}
      mtu: 1400
      addresses:
        - ${LOCAL_IP}/24
      routes:
        - to: "10.0.0.0/16"
          via: "10.1.0.1"
          metric: 100
      optional: false
      accept-ra: false
EOF

    echo "Netplan configuration created"

    # Generate and apply netplan with retry logic
    echo "Applying netplan configuration..."

    # First, generate the configuration
    if ! netplan generate; then
        echo "ERROR: netplan generate failed"
        exit 1
    fi

    # Apply with timeout and retry
    APPLY_RETRIES=3
    APPLY_SUCCESS=false
    for i in $(seq 1 $APPLY_RETRIES); do
        echo "Applying netplan (attempt $i/$APPLY_RETRIES)..."
        if timeout 30 netplan apply; then
            APPLY_SUCCESS=true
            echo "✓ Netplan applied successfully"
            break
        else
            echo "⚠ Netplan apply failed or timed out (attempt $i/$APPLY_RETRIES)"
            sleep 5
        fi
    done

    if [ "$APPLY_SUCCESS" != "true" ]; then
        echo "ERROR: Failed to apply netplan after $APPLY_RETRIES attempts"
        exit 1
    fi

    # Wait for VLAN interface to come up
    echo "Waiting for VLAN interface ${DEFAULT_IFACE}.4001 to be ready..."
    VLAN_READY=false
    for i in {1..60}; do
        if ip link show "${DEFAULT_IFACE}.4001" 2>/dev/null | grep -q "state UP"; then
            VLAN_IP=$(ip addr show "${DEFAULT_IFACE}.4001" | grep "inet " | awk '{print $2}')
            if [ -n "$VLAN_IP" ]; then
                echo "✓ VLAN interface ${DEFAULT_IFACE}.4001 is up with IP: $VLAN_IP"
                VLAN_READY=true
                break
            fi
        fi
        echo "Waiting for VLAN interface to be ready... ($i/60)"
        sleep 1
    done

    if [ "$VLAN_READY" != "true" ]; then
        echo "⚠ WARNING: VLAN interface did not come up within expected time"
        echo "Current network state:"
        ip addr
        echo ""
        echo "Routes:"
        ip route
    else
        # Verify connectivity to gateway
        echo "Verifying connectivity to gateway 10.1.0.1..."
        PING_SUCCESS=false
        for i in {1..30}; do
            if ping -c 1 -W 2 -I "${DEFAULT_IFACE}.4001" 10.1.0.1 >/dev/null 2>&1; then
                echo "✓ Successfully reached gateway 10.1.0.1"
                PING_SUCCESS=true
                break
            fi
            echo "Waiting for gateway to respond... ($i/30)"
            sleep 2
        done

        if [ "$PING_SUCCESS" != "true" ]; then
            echo "⚠ WARNING: Could not ping gateway 10.1.0.1"
            echo "This may cause connectivity issues but continuing anyway..."
        fi
    fi

    echo "Local IP configuration completed"
else
    echo "No local IP provided, skipping network configuration"
fi

# Configure CPU governor to performance
echo "Configuring CPU governor to performance..."

# Check current CPU governor
CURRENT_GOVERNOR=""
if [ -f /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor ]; then
    CURRENT_GOVERNOR=$(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 2>/dev/null || echo "")
    echo "Current CPU governor: $CURRENT_GOVERNOR"

    # Only proceed if governor needs to be changed
    if [ "$CURRENT_GOVERNOR" != "performance" ]; then
        echo "Setting CPU governor to performance"

        # Install cpufrequtils for Debian/Ubuntu systems
        echo "Installing CPU frequency utilities..."
        apt-get update
        apt-get install -y cpufrequtils

        # Set CPU governor for all CPUs immediately
        echo "Applying performance governor to all CPUs..."
        for cpu in /sys/devices/system/cpu/cpu[0-9]*; do
            if [ -f "$cpu/cpufreq/scaling_governor" ]; then
                echo "performance" > "$cpu/cpufreq/scaling_governor" 2>/dev/null || true
                echo "Set governor for $(basename $cpu): performance"
            fi
        done


        # Persist governor setting in /etc/default/cpufrequtils
        echo "Persisting CPU governor setting..."
        mkdir -p /etc/default
        echo "GOVERNOR=\"performance\"" > /etc/default/cpufrequtils

        # Enable and start cpufrequtils service
        echo "Enabling cpufrequtils service..."
        systemctl enable cpufrequtils 2>/dev/null || true
        systemctl start cpufrequtils 2>/dev/null || true

        # Verify the setting was applied
        NEW_GOVERNOR=$(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor 2>/dev/null || echo "unknown")
        if [ "$NEW_GOVERNOR" = "performance" ]; then
            echo "✓ CPU governor successfully set to performance"
        else
            echo "⚠ Warning: CPU governor may not have been set correctly. Current: $NEW_GOVERNOR"
        fi

        echo "CPU governor configuration completed"
    else
        echo "CPU governor already set to performance"
    fi
else
    echo "CPU frequency scaling not available or not supported on this system"
fi

# EXTRASCRIPTREPLACEME
`
