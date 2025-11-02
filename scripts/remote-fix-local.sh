#!/bin/bash
#
# All-in-one script to fix K3S node IP configuration
# This applies the fix, clears cache, and forces reconnection
#
# Usage: ./remote-fix-local.sh <server-ip> <node-name>
#

set -e

if [ -z "$1" ] || [ -z "$2" ]; then
    echo "Usage: $0 <server-ip> <node-name>"
    echo ""
    echo "Example: $0 157.180.103.28 scylladb-9-0c303b"
    echo ""
    echo "This script will:"
    echo "  1. Add --node-ip to K3S service file"
    echo "  2. Add K3S_NODE_IP to environment file"
    echo "  3. Stop K3S and clear its cache"
    echo "  4. Restart K3S with clean state"
    echo "  5. Delete node from cluster (if exists)"
    echo "  6. Wait for node to reconnect with correct INTERNAL-IP"
    echo ""
    exit 1
fi

SERVER_IP="$1"
NODE_NAME="$2"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=========================================="
echo "K3S Node IP Fix - Complete Workflow"
echo "=========================================="
echo ""
echo "Server IP:  $SERVER_IP"
echo "Node Name:  $NODE_NAME"
echo ""

# Check if kubectl is available
if ! command -v kubectl >/dev/null 2>&1; then
    echo "ERROR: kubectl not found"
    echo "This script needs kubectl to delete and verify the node"
    exit 1
fi

# Check if we can reach the server
if ! ssh -o ConnectTimeout=5 root@"$SERVER_IP" 'echo "SSH OK"' >/dev/null 2>&1; then
    echo "ERROR: Cannot connect to $SERVER_IP via SSH"
    echo "Make sure:"
    echo "  - SSH key is loaded (ssh-add)"
    echo "  - Server is accessible"
    echo "  - IP address is correct"
    exit 1
fi

echo "✓ SSH connection verified"
echo ""

# Step 1: Apply the fix to service file and environment
echo "=========================================="
echo "Step 1: Applying K3S node-ip fix"
echo "=========================================="
echo ""

FIX_SCRIPT=$(cat << 'REMOTE_SCRIPT_EOF'
#!/bin/bash
set -e

# Find VLAN interface and IP
VLAN_IFACE=$(ip -o link show | grep '\.4001[@:]' | awk '{print $2}' | sed 's/:$//' | sed 's/@.*//' | head -1)
LOCAL_IP=$(ip -4 addr show "$VLAN_IFACE" 2>/dev/null | grep -oP '(?<=inet\s)\d+\.\d+\.\d+\.\d+' | head -1)

if [ -z "$LOCAL_IP" ]; then
    echo "ERROR: Could not find private IP on VLAN interface"
    exit 1
fi

# Find public/external IP (non-VLAN, non-loopback interface)
EXTERNAL_IP=$(ip -4 addr show | grep -v "127.0.0.1" | grep -v "$LOCAL_IP" | grep -oP '(?<=inet\s)\d+\.\d+\.\d+\.\d+' | head -1)

if [ -z "$EXTERNAL_IP" ]; then
    echo "WARNING: Could not find external IP, will only set node-ip"
fi

echo "Private IP:  $LOCAL_IP"
echo "External IP: $EXTERNAL_IP"
echo ""

# Check if service file exists
SERVICE_FILE="/etc/systemd/system/k3s-agent.service"
if [ ! -f "$SERVICE_FILE" ]; then
    echo "ERROR: $SERVICE_FILE not found"
    exit 1
fi

# Backup service file
BACKUP="${SERVICE_FILE}.backup.$(date +%Y%m%d-%H%M%S)"
cp "$SERVICE_FILE" "$BACKUP"
echo "Backup: $BACKUP"

# Build the node arguments
if [ -n "$EXTERNAL_IP" ]; then
    NODE_ARGS="--node-ip=$LOCAL_IP --node-external-ip=$EXTERNAL_IP --flannel-iface=$VLAN_IFACE"
else
    NODE_ARGS="--node-ip=$LOCAL_IP --flannel-iface=$VLAN_IFACE"
fi

# Check if --node-ip already in service file
if grep -q "agent --node-ip=" "$SERVICE_FILE"; then
    # Update existing - remove old args and add new ones
    sed -i "s|agent --node-ip=[0-9.]*\( --node-external-ip=[0-9.]*\)\?\( --flannel-iface=[^ ]*\)\?|agent $NODE_ARGS|" "$SERVICE_FILE"
    echo "Updated existing node arguments to: $NODE_ARGS"
else
    # Add new
    sed -i "/^\s*agent\s*\\\\/s|agent|agent $NODE_ARGS|" "$SERVICE_FILE"
    echo "Added node arguments: $NODE_ARGS"
fi

# Also update environment file for consistency
ENV_FILE="/etc/systemd/system/k3s-agent.service.env"
if [ -f "$ENV_FILE" ]; then
    if grep -q "^K3S_NODE_IP=" "$ENV_FILE"; then
        sed -i "s|^K3S_NODE_IP=.*|K3S_NODE_IP='$LOCAL_IP'|" "$ENV_FILE"
    else
        echo "K3S_NODE_IP='$LOCAL_IP'" >> "$ENV_FILE"
    fi
    echo "Updated environment file"
fi

echo ""
echo "✓ Configuration updated with --node-ip=$LOCAL_IP"
REMOTE_SCRIPT_EOF
)

# Execute fix on remote server
if ! echo "$FIX_SCRIPT" | ssh root@"$SERVER_IP" 'bash -s'; then
    echo ""
    echo "ERROR: Failed to apply fix on remote server"
    exit 1
fi

echo ""

# Step 2: Stop K3S, clear cache, and restart
echo "=========================================="
echo "Step 2: Clearing K3S cache and restarting"
echo "=========================================="
echo ""

RESTART_SCRIPT=$(cat << 'RESTART_EOF'
#!/bin/bash
set -e

echo "Stopping K3S agent..."
systemctl stop k3s-agent.service
sleep 5

echo "Unmounting any busy volumes..."
umount -l /var/lib/kubelet/pods/*/volumes/* 2>/dev/null || true
umount -l /var/lib/kubelet/pods/*/volume-subpaths/* 2>/dev/null || true
sleep 2

echo "Clearing K3S cache, certificates, and keys..."
rm -rf /var/lib/rancher/k3s/agent/pod-manifests/* 2>/dev/null || true
# Remove client certificates, keys, and kubeconfig to force completely fresh registration
rm -f /var/lib/rancher/k3s/agent/client-*.crt 2>/dev/null || true
rm -f /var/lib/rancher/k3s/agent/client-*.key 2>/dev/null || true
rm -f /var/lib/rancher/k3s/agent/serving-*.crt 2>/dev/null || true
rm -f /var/lib/rancher/k3s/agent/serving-*.key 2>/dev/null || true
rm -f /var/lib/rancher/k3s/agent/kubelet.kubeconfig 2>/dev/null || true
rm -f /var/lib/rancher/k3s/agent/kubeproxy.kubeconfig 2>/dev/null || true
rm -f /var/lib/rancher/k3s/agent/k3scontroller.kubeconfig 2>/dev/null || true

echo "Reloading systemd..."
systemctl daemon-reload

echo "Starting K3S agent..."
systemctl start k3s-agent.service

sleep 3

if systemctl is-active k3s-agent.service >/dev/null 2>&1; then
    echo "✓ K3S agent restarted successfully"
else
    echo "ERROR: K3S agent failed to start"
    systemctl status k3s-agent.service --no-pager | head -20
    exit 1
fi
RESTART_EOF
)

if ! echo "$RESTART_SCRIPT" | ssh root@"$SERVER_IP" 'bash -s'; then
    echo ""
    echo "ERROR: Failed to restart K3S on remote server"
    exit 1
fi

echo ""

# Step 3: Delete node from cluster if it exists
echo "=========================================="
echo "Step 3: Removing old node registration"
echo "=========================================="
echo ""

if kubectl get node "$NODE_NAME" >/dev/null 2>&1; then
    echo "Deleting node '$NODE_NAME' from cluster..."
    kubectl delete node "$NODE_NAME"
    echo "✓ Node deleted"
else
    echo "Node not currently in cluster (may have already been deleted)"
fi

echo ""

# Step 4: Wait for node to reconnect
echo "=========================================="
echo "Step 4: Waiting for node to reconnect"
echo "=========================================="
echo ""

echo "Waiting for node '$NODE_NAME' to re-register..."
echo "(This usually takes 30-60 seconds)"
echo ""

MAX_WAIT=120
ELAPSED=0
START_TIME=$(date +%s)

while [ $ELAPSED -lt $MAX_WAIT ]; do
    if kubectl get node "$NODE_NAME" >/dev/null 2>&1; then
        echo ""
        echo "✓ Node has re-registered!"
        echo ""

        CURRENT_TIME=$(date +%s)
        WAIT_TIME=$((CURRENT_TIME - START_TIME))
        echo "Re-registration took: ${WAIT_TIME} seconds"
        echo ""

        # Wait a bit for node to stabilize
        sleep 5

        echo "Final node status:"
        echo "=================="
        kubectl get node "$NODE_NAME" -o wide
        echo ""

        # Get and validate IPs
        INTERNAL_IP=$(kubectl get node "$NODE_NAME" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
        EXTERNAL_IP=$(kubectl get node "$NODE_NAME" -o jsonpath='{.status.addresses[?(@.type=="ExternalIP")].address}')
        NODE_STATUS=$(kubectl get node "$NODE_NAME" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')

        echo "Verification:"
        echo "============="
        echo "Node Status:  $NODE_STATUS"
        echo "Internal IP:  $INTERNAL_IP"
        echo "External IP:  $EXTERNAL_IP"
        echo ""

        if [ -n "$INTERNAL_IP" ] && [ "$INTERNAL_IP" != "<none>" ]; then
            if echo "$INTERNAL_IP" | grep -q "^10\.1\.0\."; then
                echo "=========================================="
                echo "✓ SUCCESS!"
                echo "=========================================="
                echo ""
                echo "Node is now using private VLAN IP: $INTERNAL_IP"
                echo ""
                echo "Benefits:"
                echo "  • Pod-to-pod traffic uses fast private network"
                echo "  • ScyllaDB and databases will work correctly"
                echo "  • Lower latency and better performance"
                echo "  • Traffic stays on private network"
                echo ""
                exit 0
            else
                echo "⚠ WARNING: Internal IP is $INTERNAL_IP"
                echo "Expected an IP in the 10.1.0.0/24 range"
                echo ""
                exit 1
            fi
        else
            echo "=========================================="
            echo "⚠ WARNING: Internal IP is still not set"
            echo "=========================================="
            echo ""
            echo "The node reconnected but INTERNAL-IP shows: $INTERNAL_IP"
            echo ""
            echo "To troubleshoot:"
            echo "  1. Check if --node-ip is in the service file:"
            echo "     ssh root@$SERVER_IP 'grep node-ip /etc/systemd/system/k3s-agent.service'"
            echo ""
            echo "  2. Check K3S logs:"
            echo "     ssh root@$SERVER_IP 'journalctl -u k3s-agent.service -n 50'"
            echo ""
            echo "  3. Verify the process arguments:"
            echo "     ssh root@$SERVER_IP 'ps aux | grep k3s'"
            echo ""
            exit 1
        fi
    fi

    # Progress indicator
    if [ $((ELAPSED % 10)) -eq 0 ] && [ $ELAPSED -gt 0 ]; then
        echo "Still waiting... ($ELAPSED seconds)"
    fi

    sleep 2
    ELAPSED=$((ELAPSED + 2))
done

echo ""
echo "=========================================="
echo "❌ ERROR: Node did not reconnect"
echo "=========================================="
echo ""
echo "Node '$NODE_NAME' did not re-register within $MAX_WAIT seconds"
echo ""
echo "To troubleshoot:"
echo "  1. Check K3S agent status:"
echo "     ssh root@$SERVER_IP 'systemctl status k3s-agent.service'"
echo ""
echo "  2. Check K3S logs:"
echo "     ssh root@$SERVER_IP 'journalctl -u k3s-agent.service -n 100'"
echo ""
echo "  3. Verify network connectivity:"
echo "     ssh root@$SERVER_IP 'ping -c 3 10.0.0.120'"
echo ""
echo "  4. Check if K3S is trying to update instead of create:"
echo "     ssh root@$SERVER_IP 'journalctl -u k3s-agent.service | grep \"not found\"'"
echo ""
exit 1
