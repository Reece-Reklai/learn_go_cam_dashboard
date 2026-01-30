package main

import (
	"camera-dashboard-go/internal/ui"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// Version information - set by linker flags during build
var (
	Version   = "dev"
	BuildTime = "unknown"
	GoVersion = "unknown"
)

func main() {
	// Command line flags
	showVersion := flag.Bool("version", false, "Show version information")
	flag.BoolVar(showVersion, "v", false, "Show version information (shorthand)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("Camera Dashboard %s\n", Version)
		fmt.Printf("  Build time: %s\n", BuildTime)
		fmt.Printf("  Go version: %s\n", GoVersion)
		fmt.Printf("  Platform:   linux/arm64 (Raspberry Pi)\n")
		os.Exit(0)
	}

	log.Printf("[Main] Camera Dashboard %s starting...", Version)

	app := ui.NewApp()

	// Setup signal handling for clean shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("[Main] Received signal %v, cleaning up...", sig)
		app.Cleanup()
		os.Exit(0)
	}()

	app.Start()

	// Cleanup on normal exit
	app.Cleanup()
}
