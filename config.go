package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/vishvananda/netlink"
)

const (
	ConfigFilename = "config.json"
)

type (
	RunConfig struct {
		NodeID     string
		OwnIPv4    string
		OwnPort    string
		PeerSocket string
		VIP        string
		VIPAddr    netip.Addr
		VIPMask    string
		HTTPPort   string
		Interface  *net.Interface
		Link       netlink.Link
		LinkAddr   *netlink.Addr
	}

	SetupConfig struct {
		NodeID        string `json:"nodeId,omitempty"`
		OwnIPv4       string `json:"ownIPv4,omitempty"`
		OwnPort       string `json:"ownPort,omitempty"`
		PeerSocket    string `json:"peerSocket,omitempty"`
		VIP           string `json:"vip,omitempty"`
		VIPMask       string `json:"vipMask,omitempty"`
		HTTPPort      string `json:"httpPort,omitempty"`
		InterfaceName string `json:"interfaceName,omitempty"`
	}
)

func (cfg *RunConfig) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	sconfig := SetupConfig{
		VIPMask:       cfg.VIPMask,
		InterfaceName: cfg.Interface.Name,
		PeerSocket:    cfg.OwnIPv4 + ":" + cfg.OwnPort,
		HTTPPort:      cfg.HTTPPort,
	}
	_ = json.NewEncoder(w).Encode(sconfig)
}

func retrieveConfig(actvnd string) *RunConfig {
	ctx, cancel := context.WithTimeout(context.Background(), configPullTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/config", actvnd), nil)
	if err != nil {
		log.Fatalf("Config pull error: %v", err)
	}

	rsp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal(err)
	}

	defer rsp.Body.Close()

	var sc SetupConfig
	if err := json.NewDecoder(rsp.Body).Decode(&sc); err != nil {
		log.Fatal(err)
	}

	return buildConfig(sc.VIPMask, sc.InterfaceName, "Node2", sc.PeerSocket, sc.HTTPPort)
}

func buildConfig(vipmask, ifacename, nd, prskt, hprt string) *RunConfig {
	vipStr := strings.Split(vipmask, "/")[0]
	vip, err := netip.ParseAddr(vipStr)
	if err != nil {
		log.Fatalf("IP parse error: %v", err)
	}

	prt := strings.Split(prskt, ":")[1]

	link, err := netlink.LinkByName(ifacename)
	if err != nil {
		log.Fatal(err)
	}

	lnkaddr, err := netlink.ParseAddr(vipmask)
	if err != nil {
		log.Fatal(err)
	}

	iface, err := net.InterfaceByName(ifacename)
	if err != nil {
		log.Fatalf("Interface error: %v", err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		log.Fatalf("Interface IPv4 error: %v", err)
	}

	if len(addrs) < 1 {
		log.Fatal("Interface with no IPv4")
	}

	ipnet, ok := addrs[0].(*net.IPNet)
	if !ok {
		log.Fatal("Interface with non-supported IP")
	}

	cfg := &RunConfig{
		NodeID:     nd,
		OwnIPv4:    ipnet.IP.String(),
		OwnPort:    prt,
		PeerSocket: prskt,
		VIPAddr:    vip,
		VIP:        vipStr,
		VIPMask:    vipmask,
		Interface:  iface,
		Link:       link,
		LinkAddr:   lnkaddr,
		HTTPPort:   hprt,
	}

	return cfg
}

func setupConfig() *RunConfig {
	sc, ok := readConfig()
	if ok {
		return buildConfig(sc.VIPMask, sc.InterfaceName, sc.NodeID, sc.PeerSocket, sc.HTTPPort)
	}

	activeNode, ok := getEnvIfExists("ACTIVE_NODE_WS")
	if ok {
		rc := retrieveConfig(activeNode)
		rc.writeConfig()
		return rc
	}

	rc := buildConfig(getEnv("VIP_MASK"), getEnv("INTERFACE"), "Node1", getEnv("PEER_ADDR"), getEnv("HTTP_PORT"))
	rc.writeConfig()
	return rc
}

func readConfig() (SetupConfig, bool) {
	var sc SetupConfig

	exePath, err := os.Executable()
	if err != nil {
		fmt.Println("Error getting executable path:", err)
		return sc, false
	}
	exeDir := filepath.Dir(exePath)

	configPath := filepath.Join(exeDir, ConfigFilename)

	// Open the file
	file, err := os.Open(configPath)
	if err != nil {
		return sc, false
	}
	defer file.Close()

	// Decode JSON
	if err := json.NewDecoder(file).Decode(&sc); err != nil {
		return sc, false
	}

	return sc, true
}

func (cfg *RunConfig) writeConfig() {
	sc := SetupConfig{
		VIPMask:       cfg.VIPMask,
		InterfaceName: cfg.Interface.Name,
		NodeID:        cfg.NodeID,
		PeerSocket:    cfg.PeerSocket,
		HTTPPort:      cfg.HTTPPort,
	}
	jsonData, err := json.Marshal(sc)
	if err != nil {
		log.Printf("Config marshal error: %v", err)
	}

	if err := os.WriteFile(ConfigFilename, jsonData, 0644); err != nil {
		log.Printf("Write config error: %v", err)
	}
}
