package main

import (
	"camera-dashboard-go/internal/config"
	"camera-dashboard-go/internal/ui"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
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
	configPath := flag.String("config", "", "Path to config.ini (default: ./config.ini or $CAMERA_DASHBOARD_CONFIG)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("Camera Dashboard %s\n", Version)
		fmt.Printf("  Build time: %s\n", BuildTime)
		fmt.Printf("  Go version: %s\n", GoVersion)
		fmt.Printf("  Platform:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Printf("[Main] WARNING: Config load error: %v (using defaults)", err)
		cfg = config.DefaultConfig()
	}

	// Configure logging (rotating file + optional stdout)
	logCleanup, err := config.ConfigureLogging(cfg)
	if err != nil {
		log.Printf("[Main] WARNING: Logging setup error: %v", err)
	}
	if logCleanup != nil {
		defer logCleanup()
	}

	log.Printf("[Main] Camera Dashboard %s starting...", Version)
	log.Printf("[Main] Config: %dx%d @ %d FPS, dynamic=%v, slots=%d",
		cfg.CaptureWidth, cfg.CaptureHeight, cfg.CaptureFPS,
		cfg.DynamicFPSEnabled, cfg.CameraSlotCount)

	// Validate config
	ok, warnings := cfg.Validate()
	if !ok {
		log.Printf("[Main] WARNING: Config validation failed!")
	}
	for _, w := range warnings {
		log.Printf("[Main] WARNING: %s", w)
	}

	app := ui.NewApp(cfg)

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
