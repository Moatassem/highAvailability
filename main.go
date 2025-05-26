package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	cfg := setupConfig()
	defer recoverPanics(cfg)

	mystate := NewNodeState(cfg)

	StandbyState = NewNodeSS(cfg)

	setupSignalHandler(cfg)

	removeVIP(cfg)

	mystate.initializeNode(cfg)

	go mystate.udpHandler(cfg)
	go mystate.httpServer(cfg)

	mystate.activeElection(cfg)

	// select {} // Block main thread
}

func getEnv(key string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	log.Fatalf("Missing mandatory variable [%s]", key)
	return ""
}

func getEnvIfExists(key string) (string, bool) {
	return os.LookupEnv(key)
}

func setupSignalHandler(cfg *RunConfig) {
	signals := []os.Signal{
		syscall.SIGHUP,  // Hangup detected on controlling terminal
		syscall.SIGINT,  // Interrupt from keyboard (Ctrl+C)
		syscall.SIGQUIT, // Quit from keyboard (Ctrl+\)
		syscall.SIGILL,  // Illegal instruction
		syscall.SIGABRT, // Abort signal
		syscall.SIGFPE,  // Floating-point exception
		syscall.SIGKILL, // Kill signal (cannot be caught or ignored)
		syscall.SIGSEGV, // Segmentation fault
		syscall.SIGPIPE, // Broken pipe
		syscall.SIGALRM, // Timer signal
		syscall.SIGTERM, // Termination signal
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, signals...)

	go func() {
		<-sigCh
		cleanupVIPnDie(cfg, "Shutting down, cleaning VIP...")
	}()
}

func recoverPanics(cfg *RunConfig) {
	if r := recover(); r != nil {
		cleanupVIPnDie(cfg, r.(string))
	}
}
