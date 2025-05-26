package main

import (
	"log"
	"net"
	"os"
	"time"

	"github.com/mdlayher/arp"
	"github.com/mdlayher/ethernet"
	"github.com/vishvananda/netlink"
)

func manageVIP(cfg *RunConfig, add bool) error {
	if add {
		return netlink.AddrReplace(cfg.Link, cfg.LinkAddr)
	}
	return netlink.AddrDel(cfg.Link, cfg.LinkAddr)
}

func sendMultiGARPs(cfg *RunConfig) *arp.Client {
	client, err := arp.Dial(cfg.Interface)
	if err != nil {
		log.Printf("ARP client error: %v", err)
		return nil
	}

	pkt, err := arp.NewPacket(arp.OperationReply, cfg.Interface.HardwareAddr, cfg.VIPAddr, cfg.Interface.HardwareAddr, cfg.VIPAddr)

	if err != nil {
		log.Printf("ARP packet error: %v", err)
		return nil
	}

	for range GARPAttempts {
		if err := client.WriteTo(pkt, ethernet.Broadcast); err != nil {
			log.Printf("GARP reply send error: %v", err)
		}
		time.Sleep(heartbeatTimeout)
	}

	return client
}

func getVIPMAC(clnt *arp.Client, cfg *RunConfig) (string, bool) {
	if clnt == nil {
		return "N/A", false
	}
	//nolint:errcheck
	defer clnt.Close()

	deadline := time.Now().Add(heartbeatTimeout)
	_ = clnt.SetReadDeadline(deadline)

	hwaddr, err := clnt.Resolve(cfg.VIPAddr)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return "N/A", true
		}
		log.Printf("ARP request send error: %v", err)
		return "N/A", false
	}
	return hwaddr.String(), false
}

func cleanupVIPnDie(cfg *RunConfig, msg string) {
	log.Print(msg)
	_ = manageVIP(cfg, false)
	os.Exit(1)
}

func removeVIP(cfg *RunConfig) {
	err := manageVIP(cfg, false)
	if err != nil {
		log.Printf("VIP clean error: %v", err)
	}
}
