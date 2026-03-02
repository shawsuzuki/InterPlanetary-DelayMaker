package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/redis/go-redis/v9"
)

const snapLen = 65535

// ── Types ────────────────────────────────────────────────────────────────────

// link represents a unidirectional delayed network path.
type link struct {
	name      string       // human-readable, e.g., "earth→mars"
	queueKey  string       // Redis sorted set key
	configKey string       // Redis config key (seconds)
	delay     atomic.Int64 // current delay in nanoseconds
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	earthIface := flag.String("earth-iface", "veth-earth", "Earth-side interface (Mars link)")
	marsIface := flag.String("mars-iface", "veth-mars", "Mars-side interface")
	moonSrcIface := flag.String("moon-src-iface", "", "Earth-side interface (Moon link, optional)")
	moonIface := flag.String("moon-iface", "", "Moon-side interface (optional)")
	redisAddr := flag.String("redis", "localhost:6379", "Redis address")
	delayToMarsSec := flag.Int("delay-to-mars", 10, "Initial Earth→Mars delay (seconds)")
	delayToEarthSec := flag.Int("delay-to-earth", 10, "Initial Mars→Earth delay (seconds)")
	delayToMoonSec := flag.Int("delay-to-moon", 1, "Initial Earth→Moon delay (seconds)")
	delayFromMoonSec := flag.Int("delay-from-moon", 1, "Initial Moon→Earth delay (seconds)")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())

	// Connect to Redis
	rdb := redis.NewClient(&redis.Options{Addr: *redisAddr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis connection failed: %v", err)
	}

	// ── Earth ↔ Mars link ────────────────────────────────────────────────
	toMars := newLink("earth→mars", "delay:to_mars", "config:delay_to_mars", *delayToMarsSec)
	toEarth := newLink("mars→earth", "delay:to_earth", "config:delay_to_earth", *delayToEarthSec)

	rdb.Del(ctx, toMars.queueKey, toEarth.queueKey)
	setInitialConfig(ctx, rdb, toMars)
	setInitialConfig(ctx, rdb, toEarth)

	earthHandle := openHandle(*earthIface)
	marsHandle := openHandle(*marsIface)

	marsConn, err := openRawSocket(config.MarsIface)
	if err != nil {
		earthConn.Close()
		cancel()
		return nil, fmt.Errorf("failed to open mars socket: %w", err)
	}

	allLinks := []*link{toMars, toEarth}


func (d *DelayDaemon) Run() {
	log.Printf("L2 Delay Daemon started")
	log.Printf("  Earth interface: %s", d.config.EarthIface)
	log.Printf("  Mars interface:  %s", d.config.MarsIface)
	log.Printf("  Earth->Mars delay: %v", d.config.DelayToMars)
	log.Printf("  Mars->Earth delay: %v", d.config.DelayToEarth)

		log.Printf("  Earth↔Moon: %s / %s (delay: %ds / %ds)",
			*moonSrcIface, *moonIface, *delayToMoonSec, *delayFromMoonSec)
	}

	// Start receiver goroutines
	go d.receiveLoop(d.earthConn, "earth", queueToMars, d.getDelayToMars)
	go d.receiveLoop(d.marsConn, "mars", queueToEarth, d.getDelayToEarth)

	// Start sender goroutines
	go d.sendLoop(queueToMars, d.marsConn, "mars")
	go d.sendLoop(queueToEarth, d.earthConn, "earth")

	// Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("Shutting down...")
	cancel()
}

func (d *DelayDaemon) Stop() {
	d.cancel()
	d.earthConn.Close()
	d.marsConn.Close()
	d.rdb.Close()
}

func setInitialConfig(ctx context.Context, rdb *redis.Client, l *link) {
	secs := float64(time.Duration(l.delay.Load())) / float64(time.Second)
	rdb.Set(ctx, l.configKey, strconv.FormatFloat(secs, 'f', -1, 64), 0)
}

// ── Dynamic Configuration ────────────────────────────────────────────────────

func configReloadLoop(ctx context.Context, rdb *redis.Client, links []*link) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, l := range links {
				reloadDelay(ctx, rdb, l)
			}
		}
	}
}

func reloadDelay(ctx context.Context, rdb *redis.Client, l *link) {
	val, err := rdb.Get(ctx, l.configKey).Result()
	if err != nil {
		return
	}
	secs, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return
	}
	newDelay := int64(secs * float64(time.Second))
	old := l.delay.Swap(newDelay)
	if old != newDelay {
		log.Printf("[config] %s: %v → %v", l.name, time.Duration(old), time.Duration(newDelay))
	}
}

// ── Packet Reception ─────────────────────────────────────────────────────────

func (d *DelayDaemon) receiveLoop(conn net.PacketConn, sourceName, queueName string, getDelay func() time.Duration) {
	buf := make([]byte, 65535)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Set read deadline to allow checking context
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Printf("[%s] Read error: %v", sourceName, err)
			continue
		}

		if n == 0 {
			continue
		}

		// Copy frame data
		frame := make([]byte, n)
		copy(frame, buf[:n])

		// Parse for logging
		vlanID := parseVLAN(frame)
		frameInfo := describeFrame(frame)

		// Calculate send time using current delay
		delay := getDelay()
		sendTime := time.Now().Add(delay)
		sendTimeStr := strconv.FormatInt(sendTime.UnixNano(), 10)

		// Store in Redis sorted set (score = send time in nanoseconds)
		member := hex.EncodeToString(frame)
		err = d.rdb.ZAdd(d.ctx, queueName, redis.Z{
			Score:  float64(sendTime.UnixNano()),
			Member: member,
		}).Err()

		if err != nil {
			log.Printf("[%s] Redis error: %v", sourceName, err)
			continue
		}

		vlanStr := ""
		if vlanID > 0 {
			vlanStr = fmt.Sprintf(" VLAN=%d", vlanID)
		}
		log.Printf("[%s->%s] Queued %d bytes%s, send at %s | %s",
			sourceName, queueName, n, vlanStr, sendTimeStr[:10], frameInfo)
	}
}

func (d *DelayDaemon) sendLoop(queueName string, conn net.PacketConn, destName string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		now := strconv.FormatInt(time.Now().UnixNano(), 10)
		results, err := rdb.ZRangeByScore(ctx, l.queueKey, &redis.ZRangeBy{
			Min: "-inf", Max: now,
		}).Result()

		if err != nil {
			log.Printf("[%s] Redis dequeue error: %v", l.name, err)
			time.Sleep(10 * time.Millisecond)
			continue
		}

		for _, member := range results {
			frame, err := hex.DecodeString(member)
			if err != nil {
				log.Printf("[->%s] Decode error: %v", destName, err)
				continue
			}

			// Send the frame
			_, err = conn.WriteTo(frame, &rawAddr{})
			if err != nil {
				log.Printf("[->%s] Send error: %v", destName, err)
				continue
			}

			// Remove from queue
			d.rdb.ZRem(d.ctx, queueName, member)

			log.Printf("[->%s] Sent %d bytes", destName, len(frame))
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// ── Frame Parsing ────────────────────────────────────────────────────────────

func parseVLAN(frame []byte) uint16 {
	if len(frame) < 18 {
		return 0
	}
	if frame[12] == 0x81 && frame[13] == 0x00 {
		return uint16(frame[14]&0x0F)<<8 | uint16(frame[15])
	}
	return 0
}

func describeFrame(frame []byte) string {
	pkt := gopacket.NewPacket(frame, layers.LayerTypeEthernet, gopacket.Default)
	var parts []string
	if eth := pkt.Layer(layers.LayerTypeEthernet); eth != nil {
		e := eth.(*layers.Ethernet)
		parts = append(parts, fmt.Sprintf("%s→%s", e.SrcMAC, e.DstMAC))
	}
	if arp := pkt.Layer(layers.LayerTypeARP); arp != nil {
		a := arp.(*layers.ARP)
		if a.Operation == 1 {
			parts = append(parts, fmt.Sprintf("ARP-REQ who-has %v", net.IP(a.DstProtAddress)))
		} else {
			parts = append(parts, fmt.Sprintf("ARP-REPLY %v is-at %v", net.IP(a.SourceProtAddress), net.HardwareAddr(a.SourceHwAddress)))
		}
		return strings.Join(parts, " | ")
	}
	if ip := pkt.Layer(layers.LayerTypeIPv4); ip != nil {
		i := ip.(*layers.IPv4)
		parts = append(parts, fmt.Sprintf("IP %s→%s", i.SrcIP, i.DstIP))
	}
	if icmp := pkt.Layer(layers.LayerTypeICMPv4); icmp != nil {
		c := icmp.(*layers.ICMPv4)
		parts = append(parts, fmt.Sprintf("ICMP type=%d", c.TypeCode.Type()))
	}
	if tcp := pkt.Layer(layers.LayerTypeTCP); tcp != nil {
		t := tcp.(*layers.TCP)
		parts = append(parts, fmt.Sprintf("TCP %d→%d", t.SrcPort, t.DstPort))
	}
	if udp := pkt.Layer(layers.LayerTypeUDP); udp != nil {
		u := udp.(*layers.UDP)
		parts = append(parts, fmt.Sprintf("UDP %d→%d", u.SrcPort, u.DstPort))
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " | ")
}
