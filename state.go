package main

import (
	"encoding/json"
	"log"
	"maps"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

const (
	heartbeatTimeout  = 50 * time.Millisecond
	heartbeatInterval = 10 * time.Millisecond

	configPullTimeout = 30 * time.Second

	heartbeatsCount = 5
	GARPAttempts    = 5
)

var StandbyState *NodeState

type NodeState struct {
	mu            sync.RWMutex      `json:"-"`
	Data          map[string]string `json:"data"`
	IsActive      bool              `json:"isActive"`
	SelfMAC       string            `json:"SelfMAC"`
	PeerMAC       string            `json:"PeerMAC"`
	IsPeerAlive   bool              `json:"IsPeerAlive"`
	LastContact   time.Time         `json:"-"`
	isActiveFound bool              `json:"-"`
	myConn        *net.UDPConn      `json:"-"`
	myPeer        *net.UDPAddr      `json:"-"`
}

func NewNodeState(cfg *RunConfig) *NodeState {
	nds := &NodeState{
		Data:        make(map[string]string),
		SelfMAC:     cfg.Interface.HardwareAddr.String(),
		IsPeerAlive: true,
	}

	return nds
}

func NewNodeSS(cfg *RunConfig) *NodeState {
	return &NodeState{SelfMAC: cfg.Interface.HardwareAddr.String()}
}

func (nds *NodeState) GetIfActive() bool {
	nds.mu.RLock()
	defer nds.mu.RUnlock()

	return nds.IsActive
}

func (nds *NodeState) MarshalMe() ([]byte, error) {
	nds.mu.RLock()
	defer nds.mu.RUnlock()
	if nds.IsActive {
		return json.Marshal(nds)
	}
	return json.Marshal(StandbyState)
}

func (nds *NodeState) SendData(w http.ResponseWriter, _ *http.Request) {
	nds.mu.RLock()
	defer nds.mu.RUnlock()
	_ = json.NewEncoder(w).Encode(nds)
}

func (nds *NodeState) initializeNode(cfg *RunConfig) {
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
	//nolint:errcheck
	// defer conn.Close()

	nds.myConn = conn

	dscpEF := 46 << 2
	p := ipv4.NewPacketConn(conn)
	if err = p.SetTOS(dscpEF); err != nil {
		log.Printf("Failed to set IPv4 TOS: %v (may need CAP_NET_ADMIN)", err)
	}

	log.Print("Detecting Active Node...")
}

func (nds *NodeState) udpHandler(cfg *RunConfig) {
	buf := make([]byte, 4096)
	for {
		n, _, err := nds.myConn.ReadFromUDP(buf)
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
			if !nds.isActiveFound {
				nds.isActiveFound = true
				log.Printf("Active Node alive (%s), I'm the Standby Node (%s)", cfg.PeerSocket, cfg.OwnIPv4+":"+cfg.OwnPort)
				log.Printf("Virtual (floating) IP is (%s) currently tied to Hardware Address: %s", cfg.VIP, nds.PeerMAC)
			}
		}
		nds.mu.Unlock()
	}
}

func (nds *NodeState) httpServer(cfg *RunConfig) {
	http.HandleFunc("/", nds.SendData)

	http.Handle("/config", cfg)

	http.HandleFunc("/update", func(w http.ResponseWriter, r *http.Request) {
		if !nds.GetIfActive() {
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

func (nds *NodeState) activeElection(cfg *RunConfig) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	timeOuts := 0
	for range ticker.C {
		nds.sendHeartbeat()
		if nds.LastContact.IsZero() {
			timeOuts++
			if timeOuts >= heartbeatsCount {
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

func (nds *NodeState) becomeActive(cfg *RunConfig) {
	nds.mu.Lock()
	defer nds.mu.Unlock()

	if nds.IsActive {
		return
	}

	log.Printf("Assuming myself as the Active Node (%s)", cfg.OwnIPv4+":"+cfg.OwnPort)
	if err := manageVIP(cfg, true); err != nil {
		log.Printf("VIP assignment failed: %v", err)
		return
	}
	nds.IsActive = true

	go nds.seizeVIP(cfg)
}

func (nds *NodeState) seizeVIP(cfg *RunConfig) {
	client := sendMultiGARPs(cfg)

	resolvedMAC, ok := getVIPMAC(client, cfg)
	if ok {
		log.Printf("Virtual (floating) IP is (%s) currently tied to Hardware Address: %s", cfg.VIP, nds.SelfMAC)
		return
	}
	log.Printf("Virtual (floating) IP is (%s) currently tied to Hardware Address: %s - Expected: %s", cfg.VIP, resolvedMAC, nds.SelfMAC)
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
