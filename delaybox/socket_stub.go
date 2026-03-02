//go:build !linux

package main

import (
	"fmt"
	"net"
)

func openRawSocket(ifaceName string) (net.PacketConn, error) {
	return nil, fmt.Errorf("raw sockets require Linux (AF_PACKET)")
}

func htons(i uint16) uint16 {
	return (i<<8)&0xff00 | i>>8
}

// rawAddr implements net.Addr for raw socket writes
type rawAddr struct{}

func (r *rawAddr) Network() string { return "raw" }
func (r *rawAddr) String() string  { return "raw" }
