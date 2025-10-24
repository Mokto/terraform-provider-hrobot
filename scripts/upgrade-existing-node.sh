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
read -p "Continue? (yes/no): " CONFIRM

if [ "$CONFIRM" != "yes" ]; then
    echo "Aborted."
    exit 1
fi

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
echo "Step 4: Installing ARP keepalive service"
echo "=========================================="

# Get current gateway MAC address
GATEWAY_MAC=$(ip neigh show 10.1.0.1 dev "$VLAN_IFACE" 2>/dev/null | awk '{print $5}' | head -1)
if [ -z "$GATEWAY_MAC" ]; then
    echo "⚠ WARNING: Could not determine gateway MAC address from ARP cache"
    echo "Attempting to ping gateway to populate ARP cache..."
    ping -c 3 -W 2 10.1.0.1 >/dev/null 2>&1 || true
    sleep 2
    GATEWAY_MAC=$(ip neigh show 10.1.0.1 dev "$VLAN_IFACE" 2>/dev/null | awk '{print $5}' | head -1)
fi

if [ -z "$GATEWAY_MAC" ]; then
    echo "⚠ WARNING: Still could not determine gateway MAC, using default"
    GATEWAY_MAC="f2:0b:a4:d1:20:01"
else
    echo "✓ Gateway MAC address: $GATEWAY_MAC"
fi

cat > /etc/systemd/system/vlan-arp-keepalive.service << EOF
[Unit]
Description=Keep VLAN gateway ARP entry alive
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Restart=always
RestartSec=30
ExecStart=/usr/bin/bash -c 'while true; do ip neigh replace 10.1.0.1 lladdr ${GATEWAY_MAC} dev ${VLAN_IFACE} nud reachable 2>/dev/null || true; sleep 30; done'

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
