//go:build linux

package main

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

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

// rawAddr implements net.Addr for raw socket writes
type rawAddr struct{}

func (r *rawAddr) Network() string { return "raw" }
func (r *rawAddr) String() string  { return "raw" }
