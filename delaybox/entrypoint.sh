#!/bin/bash
set -e

echo "=== Delaybox Entrypoint ==="

# Cleanup function
cleanup() {
    echo "Cleaning up..."
    ip link del veth-earth 2>/dev/null || true
    ip link del veth-mars 2>/dev/null || true
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
        echo "ERROR: Could not get container PIDs after $MAX_RETRIES retries"
        exit 1
    fi
    echo "Waiting for containers to start (attempt $RETRY/$MAX_RETRIES)..."
    sleep 1
    EARTH_PID=$(docker inspect -f '{{.State.Pid}}' earth 2>/dev/null || echo "")
    MARS_PID=$(docker inspect -f '{{.State.Pid}}' mars 2>/dev/null || echo "")
done

echo "Earth PID: $EARTH_PID"
echo "Mars PID: $MARS_PID"

# Clean up any existing veth pairs
ip link del veth-earth 2>/dev/null || true
ip link del veth-mars 2>/dev/null || true

echo "Creating veth pairs..."

# Create veth pair for Earth <-> Delaybox
ip link add veth-earth type veth peer name eth0-earth

# Create veth pair for Mars <-> Delaybox  
ip link add veth-mars type veth peer name eth0-mars

# Move interfaces to container namespaces using PID directly
echo "Moving eth0-earth to Earth container (PID $EARTH_PID)..."
ip link set eth0-earth netns $EARTH_PID

echo "Moving eth0-mars to Mars container (PID $MARS_PID)..."
ip link set eth0-mars netns $MARS_PID

# Configure delaybox side
echo "Configuring delaybox interfaces..."
ip link set veth-earth up
ip link set veth-earth promisc on
ip link set veth-mars up
ip link set veth-mars promisc on

# Configure Earth using nsenter
echo "Configuring Earth network..."
nsenter -t $EARTH_PID -n ip link set eth0-earth name eth0
nsenter -t $EARTH_PID -n ip addr add 10.0.0.2/24 dev eth0
nsenter -t $EARTH_PID -n ip link set eth0 up
nsenter -t $EARTH_PID -n ip link set lo up

# Configure Mars using nsenter
echo "Configuring Mars network..."
nsenter -t $MARS_PID -n ip link set eth0-mars name eth0
nsenter -t $MARS_PID -n ip addr add 10.0.0.3/24 dev eth0
nsenter -t $MARS_PID -n ip link set eth0 up
nsenter -t $MARS_PID -n ip link set lo up

# Signal that network is ready (via docker exec)
echo "Signaling network ready to containers..."
docker exec earth touch /tmp/net_ready 2>/dev/null || true
docker exec mars touch /tmp/net_ready 2>/dev/null || true

# Mark delaybox as ready
touch /tmp/delaybox_ready

echo "=== Network Setup Complete ==="
echo "  Earth: 10.0.0.2/24 (eth0)"
echo "  Mars:  10.0.0.3/24 (eth0)"
echo "  Delaybox: veth-earth <-> veth-mars"
echo ""
echo "Starting delay daemon..."

# Start the delay daemon
exec /usr/local/bin/delaybox \
    -earth-iface veth-earth \
    -mars-iface veth-mars \
    -redis "$REDIS_ADDR" \
    -delay-to-mars "${DELAY_EARTH_TO_MARS:-10}" \
    -delay-to-earth "${DELAY_MARS_TO_EARTH:-10}"