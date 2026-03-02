#!/bin/bash
set -e

echo "=== Delaybox Entrypoint ==="

# Cleanup function
cleanup() {
    echo "Cleaning up..."
    ip link del veth-earth 2>/dev/null || true
    ip link del veth-mars 2>/dev/null || true
    ip link del veth-earth-moon 2>/dev/null || true
    ip link del veth-moon 2>/dev/null || true
    rm -f /tmp/delaybox_ready
}
trap cleanup EXIT

# Wait for containers to be ready
echo "Waiting for earth and mars containers..."
sleep 3

# Get container PIDs
EARTH_PID=$(docker inspect -f '{{.State.Pid}}' earth 2>/dev/null || echo "")
MARS_PID=$(docker inspect -f '{{.State.Pid}}' mars 2>/dev/null || echo "")

MAX_RETRIES=30
RETRY=0
while [ -z "$EARTH_PID" ] || [ "$EARTH_PID" = "0" ] || [ -z "$MARS_PID" ] || [ "$MARS_PID" = "0" ]; do
    RETRY=$((RETRY + 1))
    if [ $RETRY -ge $MAX_RETRIES ]; then
        echo "ERROR: Could not get Earth/Mars PIDs after $MAX_RETRIES retries"
        exit 1
    fi
    echo "Waiting for Earth/Mars containers (attempt $RETRY/$MAX_RETRIES)..."
    sleep 1
    EARTH_PID=$(docker inspect -f '{{.State.Pid}}' earth 2>/dev/null || echo "")
    MARS_PID=$(docker inspect -f '{{.State.Pid}}' mars 2>/dev/null || echo "")
done

echo "Earth PID: $EARTH_PID"
echo "Mars PID: $MARS_PID"

# Check for Moon container (optional)
MOON_PID=$(docker inspect -f '{{.State.Pid}}' moon 2>/dev/null || echo "")
MOON_RETRY=0
while [ -n "$MOON_PID" ] && [ "$MOON_PID" = "0" ] && [ $MOON_RETRY -lt 10 ]; do
    MOON_RETRY=$((MOON_RETRY + 1))
    sleep 1
    MOON_PID=$(docker inspect -f '{{.State.Pid}}' moon 2>/dev/null || echo "")
done

MOON_ENABLED=false
if [ -n "$MOON_PID" ] && [ "$MOON_PID" != "0" ]; then
    MOON_ENABLED=true
    echo "Moon PID: $MOON_PID"
fi

# Clean up any existing veth pairs
ip link del veth-earth 2>/dev/null || true
ip link del veth-mars 2>/dev/null || true
ip link del veth-earth-moon 2>/dev/null || true
ip link del veth-moon 2>/dev/null || true

# ── Earth ↔ Mars link ────────────────────────────────────────────────────────
echo "Creating Earth↔Mars veth pair..."

ip link add veth-earth type veth peer name eth0-earth
ip link add veth-mars type veth peer name eth0-mars

ip link set eth0-earth netns $EARTH_PID
ip link set eth0-mars netns $MARS_PID

ip link set veth-earth up
ip link set veth-earth promisc on
ip link set veth-mars up
ip link set veth-mars promisc on

nsenter -t $EARTH_PID -n ip link set eth0-earth name eth0
nsenter -t $EARTH_PID -n ip addr add 10.0.0.2/24 dev eth0
nsenter -t $EARTH_PID -n ip link set eth0 up
nsenter -t $EARTH_PID -n ip link set lo up

nsenter -t $MARS_PID -n ip link set eth0-mars name eth0
nsenter -t $MARS_PID -n ip addr add 10.0.0.3/24 dev eth0
nsenter -t $MARS_PID -n ip link set eth0 up
nsenter -t $MARS_PID -n ip link set lo up

# ── Earth ↔ Moon link (optional) ─────────────────────────────────────────────
MOON_FLAGS=""
if [ "$MOON_ENABLED" = true ]; then
    echo "Creating Earth↔Moon veth pair..."

    ip link add veth-earth-moon type veth peer name eth1-earth
    ip link add veth-moon type veth peer name eth0-moon

    ip link set eth1-earth netns $EARTH_PID
    ip link set eth0-moon netns $MOON_PID

    ip link set veth-earth-moon up
    ip link set veth-earth-moon promisc on
    ip link set veth-moon up
    ip link set veth-moon promisc on

    # Earth gets a second interface (eth1) on a different subnet for Moon
    nsenter -t $EARTH_PID -n ip link set eth1-earth name eth1
    nsenter -t $EARTH_PID -n ip addr add 10.1.0.2/24 dev eth1
    nsenter -t $EARTH_PID -n ip link set eth1 up

    nsenter -t $MOON_PID -n ip link set eth0-moon name eth0
    nsenter -t $MOON_PID -n ip addr add 10.1.0.3/24 dev eth0
    nsenter -t $MOON_PID -n ip link set eth0 up
    nsenter -t $MOON_PID -n ip link set lo up

    docker exec moon touch /tmp/net_ready 2>/dev/null || true

    MOON_FLAGS="-moon-src-iface veth-earth-moon -moon-iface veth-moon -delay-to-moon ${DELAY_EARTH_TO_MOON:-1} -delay-from-moon ${DELAY_MOON_TO_EARTH:-1}"
fi

# Signal that network is ready
docker exec earth touch /tmp/net_ready 2>/dev/null || true
docker exec mars touch /tmp/net_ready 2>/dev/null || true

touch /tmp/delaybox_ready

echo "=== Network Setup Complete ==="
echo "  Earth: 10.0.0.2/24 (eth0)"
echo "  Mars:  10.0.0.3/24 (eth0)"
if [ "$MOON_ENABLED" = true ]; then
    echo "  Moon:  10.1.0.3/24 (eth0)"
    echo "  Earth→Moon: 10.1.0.2/24 (eth1)"
fi
echo ""
echo "Starting delay daemon..."

# Start the delay daemon
exec /usr/local/bin/delaybox \
    -earth-iface veth-earth \
    -mars-iface veth-mars \
    -redis "$REDIS_ADDR" \
    -delay-to-mars "${DELAY_EARTH_TO_MARS:-10}" \
    -delay-to-earth "${DELAY_MARS_TO_EARTH:-10}" \
    $MOON_FLAGS
