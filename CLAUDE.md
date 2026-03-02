# CLAUDE.md - InterPlanetary Delay Maker

## Project Overview

L2-transparent network delay emulator for simulating interplanetary communication delays (e.g., Earth-Mars: 3-22 minutes one-way). Designed for testing Delay-Tolerant Networking (DTN) protocols.

## Architecture

```
┌─────────┐     ┌──────────────────────────────────┐     ┌─────────┐
│  Earth  │◄───►│           Delay Box              │◄───►│  Mars   │
│Container│     │  ┌─────────┐    ┌─────────────┐  │     │Container│
│10.0.0.2 │eth0 │  │ Capture │───►│ Redis Queue │  │eth0 │10.0.0.3 │
│         │◄────┼──┤ (pcap)  │    │ (Sorted Set)│──┼────►│         │
└─────────┘     │  └─────────┘    └─────────────┘  │     └─────────┘
                │       veth-earth ◄─► veth-mars   │
                └──────────────────────────────────┘
```

**Key Design Decisions:**
- L2 (Ethernet) level operation to preserve VLAN tags
- Redis Sorted Set for O(log N) time-based queue operations
- Dynamic delay configuration via Redis keys (no restart required)

## File Structure

```
.
├── CLAUDE.md              # This file
├── README.md              # User documentation
├── docker-compose.yml     # Container orchestration
└── delaybox/
    ├── Dockerfile         # Alpine + Go + libpcap
    ├── entrypoint.sh      # veth pair setup between containers
    ├── go.mod             # Go dependencies
    └── main.go            # Delay daemon (~400 lines)
```

## Tech Stack

- **Language:** Go 1.22
- **Packet Capture:** gopacket/pcap (libpcap bindings)
- **Queue:** Redis 7 (Sorted Set with nanosecond timestamps)
- **Containers:** Docker Compose with privileged mode + pid:host

## Key Commands

```bash
# Build and start
docker compose up -d --build

# View logs
docker compose logs -f delaybox

# Test connectivity (default 10s each way = 20s RTT)
docker exec earth ping 10.0.0.3

# Change delay at runtime (seconds)
docker exec redis redis-cli SET config:delay_to_mars 1200
docker exec redis redis-cli SET config:delay_to_earth 1200

# Check queue status
docker exec redis redis-cli ZCARD delay:to_mars
docker exec redis redis-cli ZCARD delay:to_earth

# Check Redis memory
docker exec redis redis-cli INFO memory | grep used_memory_human

# Full reset
docker compose down -v && docker compose up -d --build
```

## Code Structure (main.go)

| Section | Description |
|---------|-------------|
| Constants | Redis keys, pcap settings, Ethernet protocol numbers |
| Config/DelayDaemon | Type definitions |
| Daemon Lifecycle | NewDelayDaemon(), Run(), Stop() |
| Dynamic Configuration | configReloadLoop() - polls Redis every 1s |
| Packet Reception | receiveLoop() → enqueue() |
| Packet Transmission | sendLoop() → transmit() |
| Frame Parsing | parseVLAN(), describeFrame() |
| Main | Flag parsing, signal handling |

## Redis Keys

| Key | Type | Description |
|-----|------|-------------|
| `delay:to_mars` | Sorted Set | Packets queued for Mars (score = send time in ns) |
| `delay:to_earth` | Sorted Set | Packets queued for Earth |
| `config:delay_to_mars` | String | Current Earth→Mars delay in seconds |
| `config:delay_to_earth` | String | Current Mars→Earth delay in seconds |

## Known Limitations

1. **No bandwidth limiting** - Only delay, no rate control
2. **No packet loss** - All packets delivered eventually
3. **Memory bounded by Redis** - High throughput + long delay = high memory
4. **Linux only** - Requires pid:host and privileged mode

## Testing Notes

- ARP takes 2x delay (request + reply) before IP communication works
- ping with long delay: `docker exec earth ping -W 3000 10.0.0.3`
- First ping after startup takes longest (ARP resolution)

## Potential Enhancements

- [ ] VLAN-based routing (different delays per VLAN ID)
- [ ] Bandwidth throttling (token bucket)
- [ ] Packet loss injection (random drop)
- [ ] Web UI / Prometheus metrics
- [ ] Multiple endpoint support (Moon, Mars, Jupiter)
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
1. Keep main.go under 400 lines
2. Maintain clear section separation with comments
3. All packets (including ARP) must experience delay - no bypass modes
4. Dynamic configuration via Redis is essential
5. Log format: `[source->dest] action details`
