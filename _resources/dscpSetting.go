//go:build ignore
// +build ignore

package main

import (
	"log"
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func main() {
	// Choose DSCP values (shift left by 2 bits for TOS field)
	sipDSCP := 24 << 2 // CS3 (SIP signaling)
	rtpDSCP := 46 << 2 // EF (RTP media)

	// Create UDP connection
	conn, err := net.ListenPacket("udp", ":0")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	// Get local address information
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		log.Fatal("Not a UDP address")
	}

	// Set DSCP based on IP version
	if addr.IP.To4() != nil {
		// IPv4
		p := ipv4.NewPacketConn(conn)
		if err := p.SetTOS(sipDSCP); err != nil {
			log.Printf("Failed to set IPv4 TOS: %v (may need CAP_NET_ADMIN)", err)
		}
	} else {
		// IPv6
		p := ipv6.NewPacketConn(conn)
		if err := p.SetTrafficClass(rtpDSCP); err != nil {
			log.Printf("Failed to set IPv6 Traffic Class: %v", err)
		}
	}

	// Use the connection for sending/receiving...
	// Example: Send a test packet with DSCP marking
	remoteAddr, _ := net.ResolveUDPAddr("udp", "192.168.1.100:5060")
	_, err = conn.WriteTo([]byte("test"), remoteAddr)
	if err != nil {
		log.Fatal(err)
	}

	// IPv4 example with control message
	p4 := ipv4.NewPacketConn(conn)
	cm := &ipv4.ControlMessage{TOS: sipDSCP}
	_, err = p4.WriteTo([]byte("data"), cm, remoteAddr)
}
