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
	"sync"
	"syscall"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
	"github.com/redis/go-redis/v9"
)

const (
	queueToMars      = "delay:to_mars"
	queueToEarth     = "delay:to_earth"
	configKeyToMars  = "config:delay_to_mars"
	configKeyToEarth = "config:delay_to_earth"
)

type Config struct {
	EarthIface   string
	MarsIface    string
	RedisAddr    string
	DelayToMars  time.Duration
	DelayToEarth time.Duration
}

type DelayDaemon struct {
	config       Config
	rdb          *redis.Client
	earthConn    net.PacketConn
	marsConn     net.PacketConn
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.RWMutex
	delayToMars  time.Duration
	delayToEarth time.Duration
}

func main() {
	var config Config
	var delayToMarsSec, delayToEarthSec int

	flag.StringVar(&config.EarthIface, "earth-iface", "veth-earth", "Interface connected to Earth")
	flag.StringVar(&config.MarsIface, "mars-iface", "veth-mars", "Interface connected to Mars")
	flag.StringVar(&config.RedisAddr, "redis", "localhost:6379", "Redis address")
	flag.IntVar(&delayToMarsSec, "delay-to-mars", 10, "Delay Earth->Mars in seconds")
	flag.IntVar(&delayToEarthSec, "delay-to-earth", 10, "Delay Mars->Earth in seconds")
	flag.Parse()

	config.DelayToMars = time.Duration(delayToMarsSec) * time.Second
	config.DelayToEarth = time.Duration(delayToEarthSec) * time.Second

	daemon, err := NewDelayDaemon(config)
	if err != nil {
		log.Fatalf("Failed to create daemon: %v", err)
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutting down...")
		daemon.Stop()
	}()

	daemon.Run()
}

func NewDelayDaemon(config Config) (*DelayDaemon, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Connect to Redis
	rdb := redis.NewClient(&redis.Options{
		Addr: config.RedisAddr,
	})

	// Test Redis connection
	if err := rdb.Ping(ctx).Err(); err != nil {
		cancel()
		return nil, fmt.Errorf("redis connection failed: %w", err)
	}

	// Clear old queue data
	rdb.Del(ctx, queueToMars, queueToEarth)

	// Set initial delay configuration in Redis
	rdb.Set(ctx, configKeyToMars, strconv.FormatFloat(config.DelayToMars.Seconds(), 'f', -1, 64), 0)
	rdb.Set(ctx, configKeyToEarth, strconv.FormatFloat(config.DelayToEarth.Seconds(), 'f', -1, 64), 0)

	// Open raw sockets
	earthConn, err := openRawSocket(config.EarthIface)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to open earth socket: %w", err)
	}

	// Open pcap handles
	earthHandle, err := pcap.OpenLive(config.EarthIface, snapLen, true, pcap.BlockForever)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to open mars socket: %w", err)
	}

	return &DelayDaemon{
		config:       config,
		rdb:          rdb,
		earthConn:    earthConn,
		marsConn:     marsConn,
		ctx:          ctx,
		cancel:       cancel,
		delayToMars:  config.DelayToMars,
		delayToEarth: config.DelayToEarth,
	}, nil
}

		return nil, fmt.Errorf("failed to open earth interface: %w", err)
	}

	marsHandle, err := pcap.OpenLive(config.MarsIface, snapLen, true, pcap.BlockForever)
	if err != nil {
		earthHandle.Close()
		cancel()
		return nil, fmt.Errorf("failed to open mars interface: %w", err)
	}

	d := &DelayDaemon{
		config:      config,
		rdb:         rdb,
		earthHandle: earthHandle,
		marsHandle:  marsHandle,
		ctx:         ctx,
		cancel:      cancel,
	}
	d.delayToMars.Store(int64(config.DelayToMars))
	d.delayToEarth.Store(int64(config.DelayToEarth))

	return d, nil
}

func (d *DelayDaemon) Run() {
	log.Printf("L2 Delay Daemon started")
	log.Printf("  Earth interface: %s", d.config.EarthIface)
	log.Printf("  Mars interface:  %s", d.config.MarsIface)
	log.Printf("  Earth->Mars delay: %v", d.config.DelayToMars)
	log.Printf("  Mars->Earth delay: %v", d.config.DelayToEarth)
	log.Printf("  Dynamic config via Redis: SET %s <seconds>, SET %s <seconds>", configKeyMars, configKeyEarth)

	// Start config reload goroutine
	go d.configReloadLoop()

	// Start config reload goroutine
	go d.configReloadLoop()

	// Start receiver goroutines
	go d.receiveLoop(d.earthConn, "earth", queueToMars, d.getDelayToMars)
	go d.receiveLoop(d.marsConn, "mars", queueToEarth, d.getDelayToEarth)
	go d.receiveLoop(d.earthHandle, "earth", queueToMars, &d.delayToMars)
	go d.receiveLoop(d.marsHandle, "mars", queueToEarth, &d.delayToEarth)

	// Start sender goroutines
	go d.sendLoop(queueToMars, d.marsHandle, "mars")
	go d.sendLoop(queueToEarth, d.earthHandle, "earth")

	// Wait for context cancellation
	<-d.ctx.Done()
}

func (d *DelayDaemon) configReloadLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			// Check for Mars delay update
			if val, err := d.rdb.Get(d.ctx, configKeyMars).Int64(); err == nil {
				newDelay := time.Duration(val) * time.Second
				oldDelay := time.Duration(d.delayToMars.Load())
				if newDelay != oldDelay {
					d.delayToMars.Store(int64(newDelay))
					log.Printf("[CONFIG] Earth->Mars delay changed: %v -> %v", oldDelay, newDelay)
				}
			}

			// Check for Earth delay update
			if val, err := d.rdb.Get(d.ctx, configKeyEarth).Int64(); err == nil {
				newDelay := time.Duration(val) * time.Second
				oldDelay := time.Duration(d.delayToEarth.Load())
				if newDelay != oldDelay {
					d.delayToEarth.Store(int64(newDelay))
					log.Printf("[CONFIG] Mars->Earth delay changed: %v -> %v", oldDelay, newDelay)
				}
			}
		}
	}
}

func (d *DelayDaemon) Stop() {
	d.cancel()
	d.earthHandle.Close()
	d.marsHandle.Close()
	d.rdb.Close()
}

// ── Dynamic Configuration ─────────────────────────────────────────────────────

func (d *DelayDaemon) getDelayToMars() time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.delayToMars
}

func (d *DelayDaemon) getDelayToEarth() time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.delayToEarth
}

func (d *DelayDaemon) configReloadLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			d.reloadDelayConfig(configKeyToMars, &d.delayToMars, "delay_to_mars")
			d.reloadDelayConfig(configKeyToEarth, &d.delayToEarth, "delay_to_earth")
		}
	}
}

func (d *DelayDaemon) reloadDelayConfig(redisKey string, current *time.Duration, label string) {
	val, err := d.rdb.Get(d.ctx, redisKey).Result()
	if err != nil {
		return
	}
	secs, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return
	}
	newDelay := time.Duration(secs * float64(time.Second))
	d.mu.Lock()
	defer d.mu.Unlock()
	if *current != newDelay {
		log.Printf("[config] %s: %v -> %v", label, *current, newDelay)
		*current = newDelay
	}
}

// ── Packet Reception ──────────────────────────────────────────────────────────

func (d *DelayDaemon) receiveLoop(conn net.PacketConn, sourceName, queueName string, getDelay func() time.Duration) {
	buf := make([]byte, 65535)
func (d *DelayDaemon) receiveLoop(handle *pcap.Handle, sourceName, queueName string, delayPtr *atomic.Int64) {
	packetSource := gopacket.NewPacketSource(handle, handle.LinkType())
	packetSource.NoCopy = true

	for {
		select {
		case <-d.ctx.Done():
			return
		case packet, ok := <-packetSource.Packets():
			if !ok {
				return
			}

			// Get raw frame data
			frame := packet.Data()
			if len(frame) == 0 {
				continue
			}

			// Get current delay from atomic
			delay := time.Duration(delayPtr.Load())

			// Parse for logging
			vlanID := parseVLAN(frame)
			frameInfo := describeFrame(frame)

		// Parse for logging
		vlanID := parseVLAN(frame)
		frameInfo := describeFrame(frame)

		// Calculate send time using current delay
		delay := getDelay()
		sendTime := time.Now().Add(delay)
		sendTimeStr := strconv.FormatInt(sendTime.UnixNano(), 10)
			// Calculate send time
			sendTime := time.Now().Add(delay)
			sendTimeNano := sendTime.UnixNano()

			// Store in Redis sorted set (score = send time in nanoseconds)
			// Prepend timestamp to make each packet unique (even if same content)
			uniqueID := fmt.Sprintf("%d:", time.Now().UnixNano())
			member := uniqueID + hex.EncodeToString(frame)
			err := d.rdb.ZAdd(d.ctx, queueName, redis.Z{
				Score:  float64(sendTimeNano),
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
			log.Printf("[%s->queue] Queued %d bytes%s, delay=%v | %s",
				sourceName, len(frame), vlanStr, delay, frameInfo)
		}
	}
}

func (d *DelayDaemon) sendLoop(queueName string, handle *pcap.Handle, destName string) {
	for {
		select {
		case <-d.ctx.Done():
			return
		default:
		}

		now := time.Now().UnixNano()

		// Get all frames that should be sent now
		results, err := d.rdb.ZRangeByScore(d.ctx, queueName, &redis.ZRangeBy{
			Min: "-inf",
			Max: strconv.FormatInt(now, 10),
		}).Result()

		if err != nil {
			log.Printf("[->%s] Redis error: %v", destName, err)
			time.Sleep(10 * time.Millisecond)
			continue
		}

		for _, member := range results {
			// Remove the unique ID prefix (format: "timestamp:hexdata")
			parts := strings.SplitN(member, ":", 2)
			if len(parts) != 2 {
				log.Printf("[->%s] Invalid member format: %s", destName, member)
				d.rdb.ZRem(d.ctx, queueName, member)
				continue
			}
			hexData := parts[1]
			
			frame, err := hex.DecodeString(hexData)
			if err != nil {
				log.Printf("[->%s] Decode error: %v", destName, err)
				d.rdb.ZRem(d.ctx, queueName, member)
				continue
			}

			// Send the frame
			err = handle.WritePacketData(frame)
			if err != nil {
				log.Printf("[->%s] Send error: %v", destName, err)
			} else {
				log.Printf("[->%s] Sent %d bytes", destName, len(frame))
			}

			// Remove from queue
			d.rdb.ZRem(d.ctx, queueName, member)
		}

		if len(results) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
}


func parseVLAN(frame []byte) uint16 {
	if len(frame) < 18 {
		return 0
	}
	// Check for 802.1Q tag (0x8100)
	if frame[12] == 0x81 && frame[13] == 0x00 {
		return uint16(frame[14]&0x0F)<<8 | uint16(frame[15])
	}
	return 0
}

func describeFrame(frame []byte) string {
	packet := gopacket.NewPacket(frame, layers.LayerTypeEthernet, gopacket.Default)
	var parts []string
	if ethLayer := packet.Layer(layers.LayerTypeEthernet); ethLayer != nil {
		eth := ethLayer.(*layers.Ethernet)
		parts = append(parts, fmt.Sprintf("%s->%s", eth.SrcMAC, eth.DstMAC))
	}
	if arpLayer := packet.Layer(layers.LayerTypeARP); arpLayer != nil {
		arp := arpLayer.(*layers.ARP)
		if arp.Operation == 1 {
			parts = append(parts, fmt.Sprintf("ARP-REQ who-has %v", net.IP(arp.DstProtAddress)))
		} else {
			parts = append(parts, fmt.Sprintf("ARP-REPLY %v is-at %v", net.IP(arp.SourceProtAddress), net.HardwareAddr(arp.SourceHwAddress)))
		}
		return strings.Join(parts, " | ")
	}
	if ipLayer := packet.Layer(layers.LayerTypeIPv4); ipLayer != nil {
		ip := ipLayer.(*layers.IPv4)
		parts = append(parts, fmt.Sprintf("IP %s->%s", ip.SrcIP, ip.DstIP))
	}
	if icmpLayer := packet.Layer(layers.LayerTypeICMPv4); icmpLayer != nil {
		icmp := icmpLayer.(*layers.ICMPv4)
		parts = append(parts, fmt.Sprintf("ICMP type=%d", icmp.TypeCode.Type()))
	}
	if tcpLayer := packet.Layer(layers.LayerTypeTCP); tcpLayer != nil {
		tcp := tcpLayer.(*layers.TCP)
		parts = append(parts, fmt.Sprintf("TCP %d->%d", tcp.SrcPort, tcp.DstPort))
	}
	if udpLayer := packet.Layer(layers.LayerTypeUDP); udpLayer != nil {
		udp := udpLayer.(*layers.UDP)
		parts = append(parts, fmt.Sprintf("UDP %d->%d", udp.SrcPort, udp.DstPort))
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " | ")
}
	return joinParts(parts)
}

func joinParts(parts []string) string {
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += " | "
		}
		result += p
	}
	return result
}
