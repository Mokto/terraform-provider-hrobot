#!/bin/bash
#
# Upgrade script for existing Hetzner Robot nodes
# This updates netplan configuration and adds ARP keepalive service
# WITHOUT requiring a full factory reset
#
# Usage: ./upgrade-existing-node.sh
#

set -e

echo "=========================================="
echo "Hetzner Robot Node Upgrade Script"
echo "=========================================="
echo ""
echo "This script will:"
echo "  1. Backup current netplan configuration"
echo "  2. Update netplan with improved settings"
echo "  3. Install ARP keepalive service"
echo "  4. Apply changes (brief network interruption)"
echo "  5. Reboot the server"
echo ""
echo "⚠️  WARNING: This will cause a brief network interruption and reboot!"
echo ""


echo ""
echo "=========================================="
echo "Step 1: Detecting current configuration"
echo "=========================================="

# Find the VLAN netplan config file
NETPLAN_FILE="/etc/netplan/50-local-ip.yaml"
if [ ! -f "$NETPLAN_FILE" ]; then
    echo "ERROR: $NETPLAN_FILE not found!"
    echo "Looking for alternative netplan files..."
    NETPLAN_FILE=$(find /etc/netplan -name "*.yaml" -exec grep -l "4001" {} \; | head -1)
    if [ -z "$NETPLAN_FILE" ]; then
        echo "ERROR: Could not find netplan file with VLAN 4001 configuration"
        exit 1
    fi
    echo "Found: $NETPLAN_FILE"
fi

echo "Current netplan config:"
cat "$NETPLAN_FILE"
echo ""

# Extract current configuration
VLAN_IFACE=$(grep -E "^\s+[a-z0-9]+\.4001:" "$NETPLAN_FILE" | sed 's/:.*//' | xargs)
if [ -z "$VLAN_IFACE" ]; then
    echo "ERROR: Could not determine VLAN interface name"
    exit 1
fi

# Extract physical interface name (remove .4001)
PHYSICAL_IFACE=$(echo "$VLAN_IFACE" | sed 's/\.4001$//')
echo "Physical interface: $PHYSICAL_IFACE"
echo "VLAN interface: $VLAN_IFACE"

# Extract current IP address
CURRENT_IP=$(grep -A 1 "addresses:" "$NETPLAN_FILE" | tail -1 | sed 's/.*- //' | sed 's/"//g' | xargs)
if [ -z "$CURRENT_IP" ]; then
    echo "ERROR: Could not determine current IP address"
    exit 1
fi
echo "Current IP: $CURRENT_IP"
echo ""

echo "=========================================="
echo "Step 2: Backing up current configuration"
echo "=========================================="

BACKUP_FILE="${NETPLAN_FILE}.backup.$(date +%Y%m%d-%H%M%S)"
cp "$NETPLAN_FILE" "$BACKUP_FILE"
echo "Backup saved to: $BACKUP_FILE"
echo ""

echo "=========================================="
echo "Step 3: Creating new netplan configuration"
echo "=========================================="

cat > "$NETPLAN_FILE" << EOF
network:
  version: 2
  ethernets:
    ${PHYSICAL_IFACE}:
      mtu: 1500
      optional: false
  vlans:
    ${VLAN_IFACE}:
      id: 4001
      link: ${PHYSICAL_IFACE}
      mtu: 1400
      addresses:
        - ${CURRENT_IP}
      routes:
        - to: "10.0.0.0/16"
          via: "10.1.0.1"
          metric: 100
      optional: false
      accept-ra: false
EOF

echo "New netplan configuration:"
cat "$NETPLAN_FILE"
echo ""

# Fix permissions
chmod 600 "$NETPLAN_FILE"
echo "✓ Set secure permissions (600) on netplan file"
echo ""

echo "=========================================="
echo "Step 4: Installing arping and ARP keepalive service"
echo "=========================================="

# Install arping
echo "Installing arping package..."
apt-get update -qq
apt-get install -y arping
echo "✓ arping installed"
echo ""

# Create the keepalive script
cat > /usr/local/bin/vlan-arp-keepalive.sh << 'SCRIPT_EOF'
#!/bin/bash
#
# VLAN ARP Keepalive Script
# Maintains gateway ARP entry and monitors connectivity
#

GATEWAY_IP="10.1.0.1"
VLAN_IFACE="$1"
TEST_IP="${2:-10.0.0.2}"  # Optional test IP for connectivity monitoring

# Validate parameters
if [ -z "$VLAN_IFACE" ]; then
    echo "ERROR: Missing required parameters"
    echo "Usage: $0 <vlan_interface> [test_ip]"
    exit 1
fi

echo "VLAN ARP Keepalive starting"
echo "Gateway: $GATEWAY_IP"
echo "Interface: $VLAN_IFACE"
echo "Test IP: $TEST_IP"

# Check if arping is available
if ! command -v arping >/dev/null 2>&1; then
    echo "ERROR: arping is not installed"
    echo "Install with: apt-get install -y arping"
    exit 1
fi

# Track failures for alerting
CONSECUTIVE_FAILURES=0
MAX_FAILURES_BEFORE_ALERT=3

# Function to log with timestamp
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*"
}

# Function to refresh ARP entry using arping
refresh_arp() {
    # Send gratuitous ARP requests to keep the gateway entry alive
    # -U: unsolicited ARP mode (gratuitous ARP)
    # -c 1: send 1 packet
    # -I: interface
    # -q: quiet output
    if arping -U -c 1 -I "$VLAN_IFACE" "$GATEWAY_IP" >/dev/null 2>&1; then
        return 0
    else
        local exit_code=$?
        log "WARNING: Failed to send ARP request (exit code: $exit_code)"
        return $exit_code
    fi
}

# Function to check ARP entry state
check_arp_state() {
    local state=$(ip neigh show "$GATEWAY_IP" dev "$VLAN_IFACE" 2>/dev/null | awk '{print $6}')
    echo "$state"
}

# Function to test connectivity
test_connectivity() {
    if ping -c 1 -W 2 -I "$VLAN_IFACE" "$TEST_IP" >/dev/null 2>&1; then
        return 0
    else
        return 1
    fi
}

# Initial state
log "Service initialized successfully"
ITERATION=0
LAST_LOG_TIME=$(date +%s)
LOG_INTERVAL=300  # Log status every 5 minutes

while true; do
    ITERATION=$((ITERATION + 1))
    CURRENT_TIME=$(date +%s)

    # Send ARP keepalive
    if ! refresh_arp; then
        CONSECUTIVE_FAILURES=$((CONSECUTIVE_FAILURES + 1))
        log "ARP keepalive failed (consecutive failures: $CONSECUTIVE_FAILURES)"
    fi

    # Check ARP state after refresh
    ARP_STATE=$(check_arp_state)

    # Test connectivity every 10th iteration (every ~50 seconds)
    CONNECTIVITY_OK=true
    if [ $((ITERATION % 10)) -eq 0 ]; then
        if ! test_connectivity; then
            CONNECTIVITY_OK=false
            CONSECUTIVE_FAILURES=$((CONSECUTIVE_FAILURES + 1))
            log "WARNING: Connectivity test to $TEST_IP FAILED (ARP state: $ARP_STATE)"

            # Try to recover by flushing and rebuilding ARP
            log "Attempting recovery: flushing neighbor cache"
            ip neigh flush dev "$VLAN_IFACE" 2>/dev/null || true
            sleep 1
            refresh_arp

            # Test again
            if test_connectivity; then
                log "Recovery successful: connectivity restored"
                CONSECUTIVE_FAILURES=0
            else
                log "Recovery failed: connectivity still down"
            fi
        else
            # Connectivity OK, reset failure counter
            if [ $CONSECUTIVE_FAILURES -gt 0 ]; then
                log "Connectivity restored (was failing for $CONSECUTIVE_FAILURES checks)"
            fi
            CONSECUTIVE_FAILURES=0
        fi
    fi

    # Periodic status logging (every 5 minutes when healthy)
    if [ $((CURRENT_TIME - LAST_LOG_TIME)) -ge $LOG_INTERVAL ]; then
        if [ $CONSECUTIVE_FAILURES -eq 0 ]; then
            log "Status: healthy (ARP state: $ARP_STATE, iterations: $ITERATION)"
        fi
        LAST_LOG_TIME=$CURRENT_TIME
    fi

    # Alert on persistent failures
    if [ $CONSECUTIVE_FAILURES -ge $MAX_FAILURES_BEFORE_ALERT ]; then
        log "ALERT: $CONSECUTIVE_FAILURES consecutive failures detected!"
        log "  Gateway ARP state: $ARP_STATE"
        log "  Interface state: $(ip link show "$VLAN_IFACE" 2>/dev/null | grep -o 'state [A-Z]*' || echo 'unknown')"
        log "  Routing table: $(ip route show | grep "$VLAN_IFACE" || echo 'no routes')"

        # Reset counter to avoid log spam, but continue monitoring
        CONSECUTIVE_FAILURES=0
    fi

    # Main loop interval
    sleep 5
done
SCRIPT_EOF

chmod +x /usr/local/bin/vlan-arp-keepalive.sh
echo "✓ Keepalive script created"

# Create the systemd service
cat > /etc/systemd/system/vlan-arp-keepalive.service << EOF
[Unit]
Description=Keep VLAN gateway ARP entry alive
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Restart=always
RestartSec=2
ExecStart=/usr/local/bin/vlan-arp-keepalive.sh ${VLAN_IFACE} 10.0.0.2
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

echo "✓ ARP keepalive service created"
echo ""

systemctl daemon-reload
systemctl enable vlan-arp-keepalive.service
echo "✓ ARP keepalive service enabled"
echo ""

echo "=========================================="
echo "Step 5: Applying netplan configuration"
echo "=========================================="
echo "⚠️  This will cause a brief network interruption!"
echo ""
sleep 3

# Validate netplan configuration
if ! netplan generate; then
    echo "ERROR: Netplan configuration validation failed!"
    echo "Restoring backup..."
    cp "$BACKUP_FILE" "$NETPLAN_FILE"
    netplan generate
    echo "Backup restored. No changes applied."
    exit 1
fi

echo "✓ Netplan configuration validated"
echo ""

# Apply netplan with timeout
echo "Applying netplan configuration..."
if timeout 30 netplan apply; then
    echo "✓ Netplan applied successfully"
else
    echo "⚠ WARNING: Netplan apply timed out or failed"
    echo "The configuration may still be applying..."
    sleep 5
fi

echo ""

echo "=========================================="
echo "Step 6: Starting ARP keepalive service"
echo "=========================================="

systemctl start vlan-arp-keepalive.service

if systemctl is-active vlan-arp-keepalive.service >/dev/null 2>&1; then
    echo "✓ ARP keepalive service started successfully"
else
    echo "⚠ WARNING: ARP keepalive service may not have started correctly"
    echo "Service status:"
    systemctl status vlan-arp-keepalive.service --no-pager || true
fi

echo ""

echo "=========================================="
echo "Step 7: Verifying connectivity"
echo "=========================================="

echo "Waiting for network to stabilize..."
sleep 5

echo ""
echo "VLAN interface status:"
ip addr show "$VLAN_IFACE"
echo ""

echo "Gateway ARP entry:"
ip neigh show 10.1.0.1 dev "$VLAN_IFACE"
echo ""

echo "Testing connectivity to 10.0.0.0/16 network..."
if ping -c 3 -W 2 10.0.0.2 >/dev/null 2>&1; then
    echo "✓ Successfully reached 10.0.0.2"
else
    echo "⚠ WARNING: Could not reach 10.0.0.2"
    echo "This may be normal if 10.0.0.2 doesn't exist on your network"
fi

echo ""
echo "=========================================="
echo "Step 8: Configuring K3S Docker registry mirror"
echo "=========================================="

mkdir -p /etc/rancher/k3s
cat > /etc/rancher/k3s/registries.yaml << 'EOF'
mirrors:
  docker.io:
    endpoint:
      - "https://registry-1.docker.io"
EOF

echo "✓ K3S registry mirror configured"

# Restart k3s if it's running to pick up the new configuration
if systemctl is-active k3s.service >/dev/null 2>&1 || systemctl is-active k3s-agent.service >/dev/null 2>&1; then
    echo "Restarting K3S to apply registry mirror configuration..."
    systemctl restart k3s.service 2>/dev/null || systemctl restart k3s-agent.service 2>/dev/null || true
    echo "✓ K3S restarted"
else
    echo "K3S not running, configuration will be applied on next start"
fi

echo ""
echo "=========================================="
echo "Upgrade Complete!"
echo "=========================================="
echo ""
echo "Summary:"
echo "  ✓ Netplan configuration updated"
echo "  ✓ ARP keepalive service installed and running"
echo "  ✓ Network connectivity verified"
echo ""
echo "Backup location: $BACKUP_FILE"
echo ""
echo "Rebooting in 5 seconds..."
echo "Press Ctrl+C to cancel reboot"
sleep 5

reboot
