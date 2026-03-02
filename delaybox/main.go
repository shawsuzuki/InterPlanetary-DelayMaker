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
	"github.com/google/gopacket/pcap"
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
	customSrcIface := flag.String("custom-src-iface", "", "Earth-side interface (Custom link, optional)")
	customIface := flag.String("custom-iface", "", "Custom-side interface (optional)")
	redisAddr := flag.String("redis", "localhost:6379", "Redis address")
	delayToMarsSec := flag.Float64("delay-to-mars", 10, "Initial Earth→Mars delay (seconds)")
	delayToEarthSec := flag.Float64("delay-to-earth", 10, "Initial Mars→Earth delay (seconds)")
	delayToMoonSec := flag.Float64("delay-to-moon", 1.28, "Initial Earth→Moon delay (seconds)")
	delayFromMoonSec := flag.Float64("delay-from-moon", 1.28, "Initial Moon→Earth delay (seconds)")
	delayToCustomSec := flag.Float64("delay-to-custom", 5, "Initial Earth→Custom delay (seconds)")
	delayFromCustomSec := flag.Float64("delay-from-custom", 5, "Initial Custom→Earth delay (seconds)")
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

	go receiveLoop(ctx, rdb, earthHandle, toMars)
	go sendLoop(ctx, rdb, marsHandle, toMars)
	go receiveLoop(ctx, rdb, marsHandle, toEarth)
	go sendLoop(ctx, rdb, earthHandle, toEarth)

	allLinks := []*link{toMars, toEarth}

	log.Printf("  Earth↔Mars: %s / %s (delay: %gs / %gs)",
		*earthIface, *marsIface, *delayToMarsSec, *delayToEarthSec)

	// ── Earth ↔ Moon link (optional) ─────────────────────────────────────
	if *moonSrcIface != "" && *moonIface != "" {
		toMoon := newLink("earth→moon", "delay:to_moon", "config:delay_to_moon", *delayToMoonSec)
		fromMoon := newLink("moon→earth", "delay:from_moon", "config:delay_from_moon", *delayFromMoonSec)

		rdb.Del(ctx, toMoon.queueKey, fromMoon.queueKey)
		setInitialConfig(ctx, rdb, toMoon)
		setInitialConfig(ctx, rdb, fromMoon)

		moonSrcHandle := openHandle(*moonSrcIface)
		moonHandle := openHandle(*moonIface)

		go receiveLoop(ctx, rdb, moonSrcHandle, toMoon)
		go sendLoop(ctx, rdb, moonHandle, toMoon)
		go receiveLoop(ctx, rdb, moonHandle, fromMoon)
		go sendLoop(ctx, rdb, moonSrcHandle, fromMoon)

		allLinks = append(allLinks, toMoon, fromMoon)

		log.Printf("  Earth↔Moon: %s / %s (delay: %gs / %gs)",
			*moonSrcIface, *moonIface, *delayToMoonSec, *delayFromMoonSec)
	}

	// ── Earth ↔ Custom link (optional) ───────────────────────────────────
	if *customSrcIface != "" && *customIface != "" {
		toCustom := newLink("earth→custom", "delay:to_custom", "config:delay_to_custom", *delayToCustomSec)
		fromCustom := newLink("custom→earth", "delay:from_custom", "config:delay_from_custom", *delayFromCustomSec)

		rdb.Del(ctx, toCustom.queueKey, fromCustom.queueKey)
		setInitialConfig(ctx, rdb, toCustom)
		setInitialConfig(ctx, rdb, fromCustom)

		customSrcHandle := openHandle(*customSrcIface)
		customHandle := openHandle(*customIface)

		go receiveLoop(ctx, rdb, customSrcHandle, toCustom)
		go sendLoop(ctx, rdb, customHandle, toCustom)
		go receiveLoop(ctx, rdb, customHandle, fromCustom)
		go sendLoop(ctx, rdb, customSrcHandle, fromCustom)

		allLinks = append(allLinks, toCustom, fromCustom)

		log.Printf("  Earth↔Custom: %s / %s (delay: %gs / %gs)",
			*customSrcIface, *customIface, *delayToCustomSec, *delayFromCustomSec)
	}

	// Start config reload
	go configReloadLoop(ctx, rdb, allLinks)

	log.Printf("L2 Delay Daemon started (%d links)", len(allLinks))

	// Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("Shutting down...")
	cancel()
}

// ── Link Helpers ─────────────────────────────────────────────────────────────

func newLink(name, queueKey, configKey string, delaySec float64) *link {
	l := &link{
		name:      name,
		queueKey:  queueKey,
		configKey: configKey,
	}
	l.delay.Store(int64(float64(time.Second) * delaySec))
	return l
}

func openHandle(iface string) *pcap.Handle {
	h, err := pcap.OpenLive(iface, snapLen, true, pcap.BlockForever)
	if err != nil {
		log.Fatalf("pcap open %s: %v", iface, err)
	}
	return h
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

func receiveLoop(ctx context.Context, rdb *redis.Client, handle *pcap.Handle, l *link) {
	src := gopacket.NewPacketSource(handle, handle.LinkType())
	src.NoCopy = true

	for {
		select {
		case <-ctx.Done():
			return
		case pkt, ok := <-src.Packets():
			if !ok {
				return
			}
			frame := pkt.Data()
			if len(frame) == 0 {
				continue
			}

			delay := time.Duration(l.delay.Load())
			sendTime := time.Now().Add(delay)

			// Prepend nanosecond timestamp for uniqueness (even if same frame content)
			uniqueID := fmt.Sprintf("%d:", time.Now().UnixNano())
			member := uniqueID + hex.EncodeToString(frame)

			if err := rdb.ZAdd(ctx, l.queueKey, redis.Z{
				Score: float64(sendTime.UnixNano()), Member: member,
			}).Err(); err != nil {
				log.Printf("[%s] Redis enqueue error: %v", l.name, err)
				continue
			}

			vlanID := parseVLAN(frame)
			info := describeFrame(frame)
			pktType := classifyFrameType(frame)
			vlanStr := ""
			if vlanID > 0 {
				vlanStr = fmt.Sprintf(" VLAN=%d", vlanID)
			}
			log.Printf("[%s] Queued %d bytes%s delay=%v | %s", l.name, len(frame), vlanStr, delay, info)

			// Packet log for dashboard (capped at 200 per direction)
			logEntry := fmt.Sprintf("%d|%d|%s|%s", time.Now().UnixMilli(), len(frame), pktType, info)
			rdb.LPush(ctx, "pktlog:"+l.name, logEntry)
			rdb.LTrim(ctx, "pktlog:"+l.name, 0, 199)
		}
	}
}

// ── Packet Transmission ──────────────────────────────────────────────────────

func sendLoop(ctx context.Context, rdb *redis.Client, handle *pcap.Handle, l *link) {
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
			parts := strings.SplitN(member, ":", 2)
			if len(parts) != 2 {
				rdb.ZRem(ctx, l.queueKey, member)
				continue
			}
			frame, err := hex.DecodeString(parts[1])
			if err != nil {
				rdb.ZRem(ctx, l.queueKey, member)
				continue
			}
			if err := handle.WritePacketData(frame); err != nil {
				log.Printf("[%s] Send error: %v", l.name, err)
			} else {
				log.Printf("[%s] Sent %d bytes", l.name, len(frame))
			}
			rdb.ZRem(ctx, l.queueKey, member)
		}

		if len(results) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
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

func classifyFrameType(frame []byte) string {
	if len(frame) < 14 {
		return "other"
	}
	et := uint16(frame[12])<<8 | uint16(frame[13])
	hdrOff := 14
	if et == 0x8100 && len(frame) >= 18 {
		et = uint16(frame[16])<<8 | uint16(frame[17])
		hdrOff = 18
	}
	switch et {
	case 0x0806:
		// ARP: check for Gratuitous ARP (sender IP == target IP)
		// ARP header: sender IP at +14, target IP at +24
		if len(frame) >= hdrOff+28 {
			s, t := hdrOff+14, hdrOff+24
			if frame[s] == frame[t] && frame[s+1] == frame[t+1] &&
				frame[s+2] == frame[t+2] && frame[s+3] == frame[t+3] {
				return "garp"
			}
		}
		return "arp"
	case 0x86DD:
		// IPv6 Next Header at offset 6 within IPv6 header
		if len(frame) > hdrOff+6 {
			switch frame[hdrOff+6] {
			case 6:
				return "tcp"
			case 17:
				return "udp"
			case 58:
				return "icmpv6"
			}
		}
		return "other"
	case 0x0800:
		// IPv4 Protocol at offset 9 within IPv4 header
		if len(frame) > hdrOff+9 {
			switch frame[hdrOff+9] {
			case 1:
				return "icmp"
			case 6:
				return "tcp"
			case 17:
				return "udp"
			}
		}
		return "other"
	}
	return "other"
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
