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
	"syscall"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/redis/go-redis/v9"
)

const (
	queueToMars  = "delay:to_mars"
	queueToEarth = "delay:to_earth"
)

type Config struct {
	EarthIface   string
	MarsIface    string
	RedisAddr    string
	DelayToMars  time.Duration
	DelayToEarth time.Duration
}

type DelayDaemon struct {
	config      Config
	rdb         *redis.Client
	earthConn   net.PacketConn
	marsConn    net.PacketConn
	ctx         context.Context
	cancel      context.CancelFunc
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

	// Open raw sockets
	earthConn, err := openRawSocket(config.EarthIface)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to open earth socket: %w", err)
	}

	marsConn, err := openRawSocket(config.MarsIface)
	if err != nil {
		earthConn.Close()
		cancel()
		return nil, fmt.Errorf("failed to open mars socket: %w", err)
	}

	return &DelayDaemon{
		config:    config,
		rdb:       rdb,
		earthConn: earthConn,
		marsConn:  marsConn,
		ctx:       ctx,
		cancel:    cancel,
	}, nil
}

func openRawSocket(ifaceName string) (net.PacketConn, error) {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %w", ifaceName, err)
	}

	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if err != nil {
		return nil, fmt.Errorf("socket creation failed: %w", err)
	}

	addr := syscall.SockaddrLinklayer{
		Protocol: htons(syscall.ETH_P_ALL),
		Ifindex:  iface.Index,
	}

	if err := syscall.Bind(fd, &addr); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("bind failed: %w", err)
	}

	file := os.NewFile(uintptr(fd), ifaceName)
	conn, err := net.FilePacketConn(file)
	file.Close() // FilePacketConn dups the fd
	if err != nil {
		return nil, fmt.Errorf("FilePacketConn failed: %w", err)
	}

	return conn, nil
}

func htons(i uint16) uint16 {
	return (i<<8)&0xff00 | i>>8
}

func (d *DelayDaemon) Run() {
	log.Printf("L2 Delay Daemon started")
	log.Printf("  Earth interface: %s", d.config.EarthIface)
	log.Printf("  Mars interface:  %s", d.config.MarsIface)
	log.Printf("  Earth->Mars delay: %v", d.config.DelayToMars)
	log.Printf("  Mars->Earth delay: %v", d.config.DelayToEarth)

	// Start receiver goroutines
	go d.receiveLoop(d.earthConn, "earth", queueToMars, d.config.DelayToMars)
	go d.receiveLoop(d.marsConn, "mars", queueToEarth, d.config.DelayToEarth)

	// Start sender goroutines
	go d.sendLoop(queueToMars, d.marsConn, "mars")
	go d.sendLoop(queueToEarth, d.earthConn, "earth")

	// Wait for context cancellation
	<-d.ctx.Done()
}

func (d *DelayDaemon) Stop() {
	d.cancel()
	d.earthConn.Close()
	d.marsConn.Close()
	d.rdb.Close()
}

func (d *DelayDaemon) receiveLoop(conn net.PacketConn, sourceName, queueName string, delay time.Duration) {
	buf := make([]byte, 65535)

	for {
		select {
		case <-d.ctx.Done():
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

		// Check if it's an outgoing packet (we sent it) - skip to avoid loops
		// This is a simple heuristic; in production you'd track sent packets
		if isOutgoing(frame, sourceName) {
			continue
		}

		// Parse for logging
		vlanID := parseVLAN(frame)
		frameInfo := describeFrame(frame)

		// Calculate send time
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

// rawAddr implements net.Addr for raw socket writes
type rawAddr struct{}

func (r *rawAddr) Network() string { return "raw" }
func (r *rawAddr) String() string  { return "raw" }

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

func isOutgoing(frame []byte, sourceName string) bool {
	// Simple heuristic: we can't easily detect outgoing packets without
	// tracking what we've sent. For now, we rely on the fact that
	// veth pairs don't loop back packets we send.
	// In production, you might want to use BPF filters or track sent packets.
	return false
}
