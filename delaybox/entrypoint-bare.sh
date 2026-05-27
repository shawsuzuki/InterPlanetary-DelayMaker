#!/bin/sh
# Bare-metal entrypoint: skip veth/nsenter setup and run delaybox directly
# against physical NICs. Interfaces are configured by bare-metal/setup.sh
# on the host before this container starts.
set -e

: "${EARTH_IFACE:?EARTH_IFACE is required}"
: "${MARS_IFACE:?MARS_IFACE is required}"

set -- \
    -earth-iface "$EARTH_IFACE" \
    -mars-iface "$MARS_IFACE" \
    -redis "${REDIS_ADDR:-127.0.0.1:6379}" \
    -delay-to-mars "${DELAY_EARTH_TO_MARS:-10}" \
    -delay-to-earth "${DELAY_MARS_TO_EARTH:-10}"

if [ -n "$MOON_SRC_IFACE" ] && [ -n "$MOON_IFACE" ]; then
    set -- "$@" \
        -moon-src-iface "$MOON_SRC_IFACE" \
        -moon-iface "$MOON_IFACE" \
        -delay-to-moon "${DELAY_EARTH_TO_MOON:-1.28}" \
        -delay-from-moon "${DELAY_MOON_TO_EARTH:-1.28}"
fi

if [ -n "$CUSTOM_SRC_IFACE" ] && [ -n "$CUSTOM_IFACE" ]; then
    set -- "$@" \
        -custom-src-iface "$CUSTOM_SRC_IFACE" \
        -custom-iface "$CUSTOM_IFACE" \
        -delay-to-custom "${DELAY_EARTH_TO_CUSTOM:-5}" \
        -delay-from-custom "${DELAY_CUSTOM_TO_EARTH:-5}"
fi

echo "=== Delaybox (bare-metal) ==="
echo "Starting: delaybox $*"
exec /usr/local/bin/delaybox "$@"
