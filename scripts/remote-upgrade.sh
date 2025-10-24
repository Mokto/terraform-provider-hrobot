#!/bin/bash
#
# Remote upgrade wrapper script
# Copies upgrade script to remote server and executes it
#
# Usage: ./remote-upgrade.sh <server-ip>
#

set -e

if [ -z "$1" ]; then
    echo "Usage: $0 <server-ip>"
    echo ""
    echo "Example: $0 65.21.201.230"
    exit 1
fi

SERVER_IP="$1"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCAL_SCRIPT="${SCRIPT_DIR}/upgrade-existing-node.sh"
REMOTE_SCRIPT="/tmp/upgrade-existing-node.sh"

echo "=========================================="
echo "Remote Server Upgrade"
echo "=========================================="
echo ""
echo "Target server: $SERVER_IP"
echo "Local script: $LOCAL_SCRIPT"
echo ""

# Check if local script exists
if [ ! -f "$LOCAL_SCRIPT" ]; then
    echo "ERROR: Local script not found: $LOCAL_SCRIPT"
    exit 1
fi

# Skip SSH test since user handles authentication interactively
echo "Connecting to $SERVER_IP..."
echo ""

# Copy script to remote server
echo "Copying upgrade script to server..."
if ! scp -q "$LOCAL_SCRIPT" root@"$SERVER_IP":"$REMOTE_SCRIPT"; then
    echo "ERROR: Failed to copy script to server"
    exit 1
fi
echo "✓ Script copied successfully"
echo ""

# Make script executable on remote server
echo "Making script executable..."
if ! ssh root@"$SERVER_IP" "chmod +x $REMOTE_SCRIPT"; then
    echo "ERROR: Failed to make script executable"
    exit 1
fi
echo "✓ Script is now executable"
echo ""

# Execute script on remote server
echo "=========================================="
echo "Executing upgrade script on $SERVER_IP"
echo "=========================================="
echo ""
echo "⚠️  The server will reboot at the end!"
echo ""

# Execute with PTY allocation so interactive prompts work
ssh -t root@"$SERVER_IP" "$REMOTE_SCRIPT"

SSH_EXIT_CODE=$?

echo ""
echo "=========================================="
echo "Remote execution completed"
echo "=========================================="

if [ $SSH_EXIT_CODE -eq 0 ] || [ $SSH_EXIT_CODE -eq 255 ]; then
    # Exit code 255 is expected when server reboots during SSH session
    echo ""
    echo "✓ Upgrade script executed successfully"
    echo "✓ Server is rebooting..."
    echo ""
    echo "Waiting 30 seconds for server to shut down..."
    sleep 30
    echo ""
    echo "Testing server availability..."

    MAX_WAIT=300  # 5 minutes
    ELAPSED=0
    while [ $ELAPSED -lt $MAX_WAIT ]; do
        if ssh -o ConnectTimeout=5 root@"$SERVER_IP" "echo 'Server is back online'" 2>/dev/null; then
            echo ""
            echo "✓ Server is back online after reboot!"
            echo ""
            echo "Verifying upgrade..."
            ssh root@"$SERVER_IP" "systemctl is-active vlan-arp-keepalive.service && echo '✓ ARP keepalive service is running' || echo '⚠ ARP keepalive service is NOT running'"
            echo ""
            echo "=========================================="
            echo "Upgrade completed successfully!"
            echo "=========================================="
            exit 0
        fi
        echo -n "."
        sleep 5
        ELAPSED=$((ELAPSED + 5))
    done

    echo ""
    echo "⚠ WARNING: Server did not come back online within $MAX_WAIT seconds"
    echo "The server may still be booting. You can check manually:"
    echo "  ssh root@$SERVER_IP"
else
    echo "ERROR: Upgrade script failed with exit code $SSH_EXIT_CODE"
    exit $SSH_EXIT_CODE
fi
