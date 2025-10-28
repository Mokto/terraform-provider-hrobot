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
