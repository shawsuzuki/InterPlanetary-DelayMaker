# CLAUDE.md - InterPlanetary Delay Maker

## Project Overview

L2-transparent network delay emulator for simulating interplanetary communication delays (e.g., Earth-Mars: 3-22 minutes, Earth-Moon: 1.3 seconds one-way). Designed for testing Delay-Tolerant Networking (DTN) protocols.

## Architecture

```
                    ┌──────────────────────────────────┐
┌─────────┐        │           Delay Box              │        ┌─────────┐
│  Earth  │eth0    │  ┌─────────┐    ┌─────────────┐  │   eth0 │  Mars   │
│Container│◄──────►│  │ Capture │───►│ Redis Queue │──┼───────►│Container│
│10.0.0.2 │        │  │ (pcap)  │    │ (Sorted Set)│  │        │10.0.0.3 │
│         │eth1    │  └─────────┘    └─────────────┘  │   eth0 │         │
│         │◄──────►│  veth-earth ◄─► veth-mars        │        └─────────┘
└─────────┘        │  veth-earth-moon ◄─► veth-moon   │        ┌─────────┐
                   │                                   │   eth0 │  Moon   │
                   │  Each link: independent delay     │◄──────►│Container│
                   └──────────────────────────────────┘        │10.1.0.3 │
                                                               └─────────┘
```

**Key Design Decisions:**
- L2 (Ethernet) level operation to preserve VLAN tags
- Redis Sorted Set for O(log N) time-based queue operations
- Dynamic delay configuration via Redis keys (no restart required)
- Generalized `link` abstraction: each unidirectional path has its own queue and config
- Moon link is optional (auto-detected from Docker containers)

## File Structure

```
.
├── CLAUDE.md              # This file
├── README.md              # User documentation
├── docker-compose.yml     # Container orchestration (Earth, Mars, Moon, Delaybox, Redis, Dashboard)
├── delaybox/
│   ├── Dockerfile         # Alpine + Go + libpcap
│   ├── entrypoint.sh      # veth pair setup (Mars required, Moon optional)
│   ├── go.mod             # Go dependencies
│   ├── main.go            # Delay daemon (generalized link-based architecture)
│   └── main_test.go       # Unit tests (miniredis)
├── dashboard/
│   ├── Dockerfile         # Multi-stage Go build
│   ├── go.mod             # Dashboard dependencies
│   ├── main.go            # HTTP server + Redis API
│   └── index.html         # Dark-themed SPA (auto-refresh)
└── samples/
    ├── README.md           # Demo instructions
    └── mars_sol0.jpeg      # Sample Mars rover image
```

## Tech Stack

- **Language:** Go 1.22
- **Packet Capture:** gopacket/pcap (libpcap bindings)
- **Queue:** Redis 7 (Sorted Set with nanosecond timestamps)
- **Containers:** Docker Compose with privileged mode + pid:host
- **Dashboard:** Embedded HTML served by Go HTTP server

## Key Commands

```bash
# Build and start
docker compose up -d --build

# View logs
docker compose logs -f delaybox

# Test Mars connectivity (default 10s each way = 20s RTT)
docker exec earth ping -W 30 10.0.0.3

# Test Moon connectivity (~1s each way = 2s RTT)
docker exec earth ping -W 5 10.1.0.3

# Change delay at runtime (seconds)
docker exec redis redis-cli SET config:delay_to_mars 1200
docker exec redis redis-cli SET config:delay_to_earth 1200
docker exec redis redis-cli SET config:delay_to_moon 1.3
docker exec redis redis-cli SET config:delay_from_moon 1.3

# Check queue status
docker exec redis redis-cli ZCARD delay:to_mars
docker exec redis redis-cli ZCARD delay:to_moon

# Dashboard
open http://localhost:8080

# Full reset
docker compose down -v && docker compose up -d --build
```

## Code Structure (main.go)

| Section | Description |
|---------|-------------|
| Types | `link` struct (name, queueKey, configKey, atomic delay) |
| Main | Flag parsing, link setup (Mars required, Moon optional), signal handling |
| Link Helpers | `newLink()`, `openHandle()`, `setInitialConfig()` |
| Dynamic Configuration | `configReloadLoop()` - polls Redis every 1s for all links |
| Packet Reception | `receiveLoop()` - pcap capture → Redis enqueue |
| Packet Transmission | `sendLoop()` - Redis dequeue → pcap inject |
| Frame Parsing | `parseVLAN()`, `describeFrame()` |

## Redis Keys

| Key | Type | Description |
|-----|------|-------------|
| `delay:to_mars` | Sorted Set | Packets queued for Mars (score = send time in ns) |
| `delay:to_earth` | Sorted Set | Packets queued for Earth (from Mars) |
| `delay:to_moon` | Sorted Set | Packets queued for Moon |
| `delay:from_moon` | Sorted Set | Packets queued for Earth (from Moon) |
| `config:delay_to_mars` | String | Current Earth→Mars delay in seconds |
| `config:delay_to_earth` | String | Current Mars→Earth delay in seconds |
| `config:delay_to_moon` | String | Current Earth→Moon delay in seconds |
| `config:delay_from_moon` | String | Current Moon→Earth delay in seconds |

## Network Layout

| Container | Interface | IP Address | Subnet |
|-----------|-----------|------------|--------|
| Earth | eth0 | 10.0.0.2 | 10.0.0.0/24 (Mars link) |
| Earth | eth1 | 10.1.0.2 | 10.1.0.0/24 (Moon link) |
| Mars | eth0 | 10.0.0.3 | 10.0.0.0/24 |
| Moon | eth0 | 10.1.0.3 | 10.1.0.0/24 |

## Known Limitations

1. **No bandwidth limiting** - Only delay, no rate control
2. **No packet loss** - All packets delivered eventually
3. **Memory bounded by Redis** - High throughput + long delay = high memory
4. **Linux only** - Requires pid:host and privileged mode

## Testing Notes

- ARP takes 2x delay (request + reply) before IP communication works
- ping with long delay: `docker exec earth ping -W 3000 10.0.0.3`
- First ping after startup takes longest (ARP resolution)
- Moon ping resolves quickly: `docker exec earth ping -W 5 10.1.0.3`

## Potential Enhancements

- [ ] VLAN-based routing (different delays per VLAN ID)
- [ ] Bandwidth throttling (token bucket)
- [ ] Packet loss injection (random drop)
- [ ] Prometheus metrics export
- [ ] Additional endpoints (Jupiter, Saturn)
- [ ] Physical NIC support (not just Docker containers)

## Common Issues

**Packets queued but not sent:**
- Check if send time is in the future: `redis-cli ZRANGE delay:to_mars 0 0 WITHSCORES`
- Compare with current time: `date +%s%N`

**ARP not resolving:**
- ARP needs full round-trip (2x delay)
- Check both queue sizes

**Container network not ready:**
- Verify veth pairs exist: `ip link show | grep veth`
- Check entrypoint.sh logs

**Moon not showing in dashboard:**
- Moon is auto-detected; ensure `moon` container is running
- Check `docker inspect -f '{{.State.Pid}}' moon`

## Development Workflow

```bash
# Edit main.go, then:
docker compose down
docker compose build --no-cache delaybox
docker compose up -d

# Quick iteration (if only Go code changed):
docker compose restart delaybox
```

## Context for Claude Code

This project was developed for WIDE Project Space-WG to demonstrate interplanetary network delays. The primary use case is testing ION-DTN and other DTN implementations under realistic Mars communication conditions.

The owner (Shota) is a PhD student researching DTN routing (IR-CGR). This tool supports his research by providing a realistic delay environment for protocol testing.

When making changes:
1. Maintain clear section separation with comments
2. All packets (including ARP) must experience delay - no bypass modes
3. Dynamic configuration via Redis is essential
4. Log format: `[source->dest] action details`
