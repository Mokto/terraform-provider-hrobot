#!/bin/bash
#
# Multi-server upgrade script
# Runs upgrade-existing-node.sh on multiple servers in sequence
#
# Usage: ./upgrade-existing-nodes.sh <server-ip-1> <server-ip-2> ... <server-ip-n>
#

set -e

if [ $# -eq 0 ]; then
    echo "Usage: $0 <server-ip-1> <server-ip-2> ... <server-ip-n>"
    echo ""
    echo "Example: $0 10.1.0.10 10.1.0.11 10.1.0.12"
    echo ""
    echo "This script will upgrade multiple servers sequentially."
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REMOTE_UPGRADE_SCRIPT="${SCRIPT_DIR}/remote-upgrade.sh"

# Check if remote-upgrade.sh exists
if [ ! -f "$REMOTE_UPGRADE_SCRIPT" ]; then
    echo "ERROR: remote-upgrade.sh not found at: $REMOTE_UPGRADE_SCRIPT"
    exit 1
fi

SERVERS=("$@")
TOTAL_SERVERS=${#SERVERS[@]}
SUCCESSFUL_UPGRADES=0
FAILED_UPGRADES=0
FAILED_SERVERS=()

echo "=========================================="
echo "Multi-Server Upgrade"
echo "=========================================="
echo ""
echo "Total servers to upgrade: $TOTAL_SERVERS"
echo "Servers: ${SERVERS[*]}"
echo ""
echo "Press Ctrl+C to abort"
echo ""
sleep 3

for i in "${!SERVERS[@]}"; do
    SERVER="${SERVERS[$i]}"
    SERVER_NUM=$((i + 1))

    echo ""
    echo "=========================================="
    echo "[$SERVER_NUM/$TOTAL_SERVERS] Upgrading server: $SERVER"
    echo "=========================================="
    echo ""

    if "$REMOTE_UPGRADE_SCRIPT" "$SERVER"; then
        SUCCESSFUL_UPGRADES=$((SUCCESSFUL_UPGRADES + 1))
        echo ""
        echo "✓ [$SERVER_NUM/$TOTAL_SERVERS] $SERVER upgraded successfully"
        echo ""
    else
        FAILED_UPGRADES=$((FAILED_UPGRADES + 1))
        FAILED_SERVERS+=("$SERVER")
        echo ""
        echo "✗ [$SERVER_NUM/$TOTAL_SERVERS] $SERVER upgrade FAILED"
        echo ""
        echo "Continue with remaining servers? (yes/no)"
        read -r CONTINUE
        if [ "$CONTINUE" != "yes" ]; then
            echo "Aborted by user"
            break
        fi
    fi

    # Wait a bit between servers to avoid overwhelming the network
    if [ $SERVER_NUM -lt $TOTAL_SERVERS ]; then
        echo "Waiting 10 seconds before next server..."
        sleep 10
    fi
done

echo ""
echo "=========================================="
echo "Multi-Server Upgrade Summary"
echo "=========================================="
echo ""
echo "Total servers: $TOTAL_SERVERS"
echo "Successful: $SUCCESSFUL_UPGRADES"
echo "Failed: $FAILED_UPGRADES"
echo ""

if [ $FAILED_UPGRADES -gt 0 ]; then
    echo "Failed servers:"
    for server in "${FAILED_SERVERS[@]}"; do
        echo "  - $server"
    done
    echo ""
    exit 1
else
    echo "✓ All servers upgraded successfully!"
    echo ""
    exit 0
fi
