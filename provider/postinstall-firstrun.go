package provider

// postinstallScript is the comprehensive post-install script for LUKS encryption setup
const postinstallFirstRunScript = `#!/bin/bash

LOCAL_IP="LOCALIPADDRESSREPLACEME"

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

    # Create netplan configuration
    mkdir -p /etc/netplan
    cat > /etc/netplan/50-local-ip.yaml << EOF
network:
  version: 2
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
EOF

    echo "Netplan configuration created"

    # Generate and apply netplan
    echo "Applying netplan configuration..."
    netplan generate
    netplan apply

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
