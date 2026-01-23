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

const (
	queueToMars    = "delay:to_mars"
	queueToEarth   = "delay:to_earth"
	configKeyMars  = "config:delay_to_mars"
	configKeyEarth = "config:delay_to_earth"
	snapLen        = 65535
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
	earthHandle  *pcap.Handle
	marsHandle   *pcap.Handle
	ctx          context.Context
	cancel       context.CancelFunc
	delayToMars  atomic.Int64 // nanoseconds
	delayToEarth atomic.Int64 // nanoseconds
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

	// Store initial delay config in Redis
	rdb.Set(ctx, configKeyMars, int64(config.DelayToMars.Seconds()), 0)
	rdb.Set(ctx, configKeyEarth, int64(config.DelayToEarth.Seconds()), 0)

	// Open pcap handles
	earthHandle, err := pcap.OpenLive(config.EarthIface, snapLen, true, pcap.BlockForever)
	if err != nil {
		cancel()
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

	// Start receiver goroutines
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
		return joinParts(parts)
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