#!/bin/bash
# Boot-time helper: reconfigure physical NICs (these settings don't persist
# across reboots) and bring up the bare-metal delaybox stack.
#
# Used both by setup.sh (initial run) and by the systemd unit (on every boot).
# Reads NIC names, optional VLAN IDs, and delay values from .env at the repo
# root, which must already exist (created by setup.sh on first run).
#
# If EARTH_VLAN / MARS_VLAN are set, the corresponding 802.1Q sub-interfaces
# (e.g., enp1s0.2) are created and the delaybox is pointed at them instead of
# the raw physical NIC. The physical NIC must be a trunk on the switch side
# delivering tagged frames for those VLANs.
set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ENV_FILE="$REPO_ROOT/.env"

if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: must be run as root" >&2
    exit 1
fi

if [ ! -f "$ENV_FILE" ]; then
    echo "ERROR: $ENV_FILE not found. Run bare-metal/setup.sh first." >&2
    exit 1
fi

# shellcheck disable=SC1090
set -a; . "$ENV_FILE"; set +a

if [ -z "$EARTH_IFACE" ] || [ -z "$MARS_IFACE" ]; then
    echo "ERROR: EARTH_IFACE and MARS_IFACE must be set in $ENV_FILE" >&2
    exit 1
fi

# ── NIC configuration helpers ───────────────────────────────────────────────
# Track which physical NICs we've already configured so single-trunk setups
# (EARTH_IFACE == MARS_IFACE) don't repeat the work.
CONFIGURED_PHYS=""

configure_phys() {
    local iface="$1"
    case " $CONFIGURED_PHYS " in
        *" $iface "*) return 0 ;;
    esac
    if ! ip link show "$iface" >/dev/null 2>&1; then
        echo "ERROR: interface '$iface' not found" >&2
        exit 1
    fi
    echo "Configuring $iface (physical)..."
    ip addr flush dev "$iface" || true
    ip link set "$iface" up
    ip link set "$iface" promisc on
    # Disable offloads so pcap sees actual on-wire frames.
    ethtool -K "$iface" gro off lro off tso off gso off rx off tx off 2>/dev/null || true
    # Disable hardware VLAN offload so the kernel handles tag insert/strip
    # in a way that's predictable for VLAN sub-interfaces + pcap.
    ethtool -K "$iface" rxvlan off txvlan off 2>/dev/null || true
    CONFIGURED_PHYS="$CONFIGURED_PHYS $iface"
}

ensure_vlan_subif() {
    local iface="$1" vlan="$2"
    if [ "$vlan" -lt 1 ] || [ "$vlan" -gt 4094 ]; then
        echo "ERROR: invalid VLAN id '$vlan' (must be 1-4094)" >&2
        exit 1
    fi
    local subif="${iface}.${vlan}"
    if ! ip link show "$subif" >/dev/null 2>&1; then
        echo "Creating VLAN sub-interface $subif (vlan id $vlan)..." >&2
        ip link add link "$iface" name "$subif" type vlan id "$vlan"
    fi
    ip addr flush dev "$subif" || true
    ip link set "$subif" up
    ip link set "$subif" promisc on
    echo "$subif"
}

# effective_iface: configure physical NIC, optionally create VLAN sub-if,
# echo the interface name the delaybox should pcap against.
effective_iface() {
    local iface="$1" vlan="$2"
    [ -z "$iface" ] && return 0
    configure_phys "$iface" >&2
    if [ -n "$vlan" ]; then
        ensure_vlan_subif "$iface" "$vlan"
    else
        echo "$iface"
    fi
}

# ── Configure each link ─────────────────────────────────────────────────────
EARTH_EFF=$(effective_iface "$EARTH_IFACE" "$EARTH_VLAN")
MARS_EFF=$(effective_iface  "$MARS_IFACE"  "$MARS_VLAN")
MOON_SRC_EFF=$(effective_iface "$MOON_SRC_IFACE" "$MOON_SRC_VLAN")
MOON_EFF=$(effective_iface     "$MOON_IFACE"     "$MOON_VLAN")
CUSTOM_SRC_EFF=$(effective_iface "$CUSTOM_SRC_IFACE" "$CUSTOM_SRC_VLAN")
CUSTOM_EFF=$(effective_iface     "$CUSTOM_IFACE"     "$CUSTOM_VLAN")

sysctl -w net.ipv4.ip_forward=0 >/dev/null

# ── Export effective interface names for docker compose ────────────────────
# Compose's `- EARTH_IFACE` short form passes the shell-env value into the
# container, overriding what .env had (per docker compose precedence rules).
export EARTH_IFACE="$EARTH_EFF"
export MARS_IFACE="$MARS_EFF"
export MOON_SRC_IFACE="$MOON_SRC_EFF"
export MOON_IFACE="$MOON_EFF"
export CUSTOM_SRC_IFACE="$CUSTOM_SRC_EFF"
export CUSTOM_IFACE="$CUSTOM_EFF"

echo "Delaybox will use: earth=$EARTH_IFACE mars=$MARS_IFACE"
[ -n "$MOON_SRC_IFACE" ] && echo "                   moon=$MOON_SRC_IFACE/$MOON_IFACE"
[ -n "$CUSTOM_SRC_IFACE" ] && echo "                   custom=$CUSTOM_SRC_IFACE/$CUSTOM_IFACE"

cd "$REPO_ROOT"
docker compose -f docker-compose.bare.yml up -d
