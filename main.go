package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"maps"

	"github.com/mdlayher/arp"
	"github.com/mdlayher/ethernet"
	"github.com/vishvananda/netlink"
)

const (
	heartbeatTimeout  = 50 * time.Millisecond
	heartbeatInterval = 10 * time.Millisecond

	// heartbeatTimeout  = 10 * time.Second
	// heartbeatInterval = 1 * time.Second
	heartbeatMissed = 10
	GARPAttempts    = 10
)

var StandbyState *NodeState

type NodeState struct {
	mu          sync.RWMutex      `json:"-"`
	Data        map[string]string `json:"data"`
	IsActive    bool              `json:"isActive"`
	SelfMAC     string            `json:"SelfMAC"`
	PeerMAC     string            `json:"PeerMAC"`
	IsPeerAlive bool              `json:"IsPeerAlive"`
	LastContact time.Time         `json:"-"`
	myConn      *net.UDPConn      `json:"-"`
	myPeer      *net.UDPAddr      `json:"-"`
	isSet       bool              `json:"-"`
}

func NewNodeState(cfg Config) *NodeState {
	nds := &NodeState{
		Data:        make(map[string]string),
		SelfMAC:     cfg.Interface.HardwareAddr.String(),
		IsPeerAlive: true,
	}

	return nds
}

func NewNodeSS(cfg Config) *NodeState {
	return &NodeState{SelfMAC: cfg.Interface.HardwareAddr.String()}
}

func (nds *NodeState) MarshalMe() ([]byte, error) {
	if nds.IsActive {
		nds.mu.RLock()
		defer nds.mu.RUnlock()

		return json.Marshal(nds)
	}
	return json.Marshal(StandbyState)
}

func (nds *NodeState) SendData(w http.ResponseWriter, _ *http.Request) {
	nds.mu.RLock()
	defer nds.mu.RUnlock()
	_ = json.NewEncoder(w).Encode(nds)
}

type Config struct {
	NodeID     string
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

func validateEnvVars() (netip.Addr, string, string, netlink.Link, *netlink.Addr, *net.Interface) {
	ifacename := getEnv("INTERFACE", "ens33")
	vipmask := getEnv("VIPMask", "192.168.1.100/24")

	ipStr := strings.Split(vipmask, "/")[0]
	vip, err := netip.ParseAddr(ipStr)
	if err != nil {
		log.Fatalf("IP parse error: %v", err)
	}

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

	return vip, ipStr, vipmask, link, lnkaddr, iface
}

func main() {
	vip, vipaddr, vipmask, lnk, lnkaddr, iface := validateEnvVars()

	cfg := Config{
		NodeID:     getEnv("NODE_ID", "node1"),
		OwnPort:    getEnv("OWN_PORT", "9999"),
		PeerSocket: getEnv("PEER_ADDR", "192.168.1.2:9999"),
		VIPAddr:    vip,
		VIP:        vipaddr,
		VIPMask:    vipmask,
		Interface:  iface,
		Link:       lnk,
		LinkAddr:   lnkaddr,
		HTTPPort:   getEnv("HTTP_PORT", "8080"),
	}

	mystate := NewNodeState(cfg)

	StandbyState = NewNodeSS(cfg)

	// Setup signal handler
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Printf("Shutting down, cleaning VIP...")
		_ = manageVIP(cfg, false)
		os.Exit(0)
	}()

	_ = manageVIP(cfg, false)
	log.Print("VIP clean successful")

	log.Print("Detecting Active Node...")

	go mystate.udpListener(cfg)
	go mystate.httpServer(cfg)

	mystate.activeElection(cfg)

	// select {} // Block main thread
}

func (nds *NodeState) udpListener(cfg Config) {
	addr, err := net.ResolveUDPAddr("udp", ":"+cfg.OwnPort)
	if err != nil {
		log.Fatal("Local UDP resolve error:", err)
	}

	raddr, err := net.ResolveUDPAddr("udp", cfg.PeerSocket)
	if err != nil {
		log.Fatal("Remote UDP resolve error:", err)
	}

	nds.myPeer = raddr

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatal("UDP listen error:", err)
	}
	defer conn.Close()

	nds.myConn = conn

	buf := make([]byte, 4096)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("UDP read error: %v", err)
			continue
		}

		var receivedState NodeState
		if err := json.Unmarshal(buf[:n], &receivedState); err != nil {
			log.Printf("State unmarshal error: %v", err)
			continue
		}

		nds.mu.Lock()
		nds.LastContact = time.Now()
		nds.PeerMAC = receivedState.SelfMAC
		if !nds.IsPeerAlive {
			log.Print("Peer Node alive!")
			nds.IsPeerAlive = true
		}
		if receivedState.IsActive && !nds.IsActive {
			nds.Data = receivedState.Data
			if !nds.isSet {
				nds.isSet = true
				log.Print("Active Node alive, I'm the Standby Node")
				log.Printf("Virtual (floating) IP is (%s) currently tied to Hardware Address: %s", cfg.VIP, nds.PeerMAC)
			}
		}
		nds.mu.Unlock()
	}
}

func (nds *NodeState) httpServer(cfg Config) {
	http.HandleFunc("/", nds.SendData)

	http.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {
		if !nds.IsActive {
			http.Error(w, "Not Active Node", http.StatusConflict)
			return
		}

		var update map[string]string
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		nds.mu.Lock()
		maps.Copy(nds.Data, update)
		nds.mu.Unlock()

		nds.syncStateToPeer()
		w.WriteHeader(http.StatusAccepted)
	})

	// log.Printf("HTTP server starting on port %s", cfg.HTTPPort)
	log.Fatal(http.ListenAndServe(":"+cfg.HTTPPort, nil))
}

func (nds *NodeState) activeElection(cfg Config) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	timeOuts := 0
	for range ticker.C {
		nds.sendHeartbeat()
		if nds.LastContact.IsZero() {
			timeOuts++
			if timeOuts >= heartbeatMissed {
				nds.setPeerStatus(false, "Peer Node undetected!")
				timeOuts = 0
				nds.becomeActive(cfg)
			}
		} else {
			timeOuts = 0
			if time.Since(nds.LastContact) >= heartbeatTimeout {
				nds.setPeerStatus(false, "Peer Node dead!")

				nds.becomeActive(cfg)
			}
		}
	}
}

func (nds *NodeState) setPeerStatus(sts bool, msg string) {
	nds.mu.Lock()
	defer nds.mu.Unlock()

	if nds.IsPeerAlive != sts {
		log.Print(msg)
	}
	nds.IsPeerAlive = sts
}

func (nds *NodeState) becomeActive(cfg Config) {
	nds.mu.Lock()
	defer nds.mu.Unlock()

	if nds.IsActive {
		return
	}

	log.Print("Assuming myself as the Active Node")
	nds.IsActive = true
	if err := manageVIP(cfg, true); err != nil {
		log.Printf("VIP assignment failed: %v", err)
		return
	}
	for range GARPAttempts {
		nds.sendGARP(cfg)
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("Virtual (floating) IP is (%s) currently tied to Hardware Address: %s", cfg.VIP, nds.SelfMAC)
}

func (nds *NodeState) sendHeartbeat() {
	stateData, err := nds.MarshalMe()
	if err != nil {
		log.Printf("State marshal error: %v", err)
		return
	}

	if _, err := nds.myConn.WriteToUDP(stateData, nds.myPeer); err != nil {
		log.Printf("Heartbeat send error: %v", err)
	}
}

func (nds *NodeState) syncStateToPeer() {
	stateData, err := nds.MarshalMe()
	if err != nil {
		log.Printf("State marshal error: %v", err)
		return
	}

	if _, err := nds.myConn.WriteToUDP(stateData, nds.myPeer); err != nil {
		log.Printf("State sync send error: %v", err)
	}
}

func manageVIP(cfg Config, add bool) error {
	if add {
		return netlink.AddrReplace(cfg.Link, cfg.LinkAddr)
	}
	return netlink.AddrDel(cfg.Link, cfg.LinkAddr)
}

func (nds *NodeState) sendGARP(cfg Config) {
	client, err := arp.Dial(cfg.Interface)
	if err != nil {
		log.Printf("ARP client error: %v", err)
		return
	}
	defer client.Close()

	pkt, err := arp.NewPacket(arp.OperationReply, cfg.Interface.HardwareAddr, cfg.VIPAddr, cfg.Interface.HardwareAddr, cfg.VIPAddr)

	if err != nil {
		log.Printf("ARP packet error: %v", err)
		return
	}

	if err := client.WriteTo(pkt, ethernet.Broadcast); err != nil {
		log.Printf("GARP send error: %v", err)
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
