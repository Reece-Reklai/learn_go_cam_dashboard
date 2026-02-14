# Camera Dashboard Makefile
# Builds for Raspberry Pi (requires CGO for Fyne GUI framework)
#
# IMPORTANT: Fyne uses OpenGL/GLES via CGO, so CGO_ENABLED=1 is required.
# Cross-compilation requires a cross-compiler toolchain. For simplicity,
# build natively on the target Raspberry Pi.

# Build settings
APP_NAME := camera-dashboard
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GO_VERSION := $(shell go version | cut -d' ' -f3)

# Linker flags for smaller binary and version info
LDFLAGS := -s -w \
	-X 'main.Version=$(VERSION)' \
	-X 'main.BuildTime=$(BUILD_TIME)' \
	-X 'main.GoVersion=$(GO_VERSION)'

# Build directories
BUILD_DIR := build
RELEASE_DIR := release

.PHONY: all build clean release release-optimized install run run-log stop status package help

# Default target
all: build

# Build for current platform (development)
build:
	@echo "Building $(APP_NAME) for current platform..."
	CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o $(APP_NAME) .
	@echo "Built: $(APP_NAME)"
	@ls -lh $(APP_NAME)

# Build optimized release binary (native build on Pi)
release: release-optimized

# Optimized native build (run this ON the Raspberry Pi)
release-optimized:
	@echo "Building optimized release for current platform..."
	@echo "NOTE: Run this directly on your Raspberry Pi for best results"
	@mkdir -p $(RELEASE_DIR)
	CGO_ENABLED=1 go build \
		-ldflags "$(LDFLAGS)" \
		-trimpath \
		-o $(RELEASE_DIR)/$(APP_NAME) .
	@echo ""
	@echo "Built: $(RELEASE_DIR)/$(APP_NAME)"
	@ls -lh $(RELEASE_DIR)/$(APP_NAME)
	@file $(RELEASE_DIR)/$(APP_NAME)

# Debug build with symbols (for profiling/debugging)
debug:
	@echo "Building debug version..."
	CGO_ENABLED=1 go build -o $(APP_NAME)-debug .
	@echo "Built: $(APP_NAME)-debug"
	@ls -lh $(APP_NAME)-debug

# Install to /usr/local/bin (requires sudo)
install: release-optimized
	@echo "Installing $(APP_NAME) to /usr/local/bin..."
	sudo cp $(RELEASE_DIR)/$(APP_NAME) /usr/local/bin/$(APP_NAME)
	sudo chmod +x /usr/local/bin/$(APP_NAME)
	@echo "Installed to /usr/local/bin/$(APP_NAME)"
	@echo ""
	@/usr/local/bin/$(APP_NAME) -version || true

# Install systemd service for auto-start
install-service: install
	@echo "Installing systemd service..."
	sudo cp camera-dashboard.service /etc/systemd/system/
	sudo systemctl daemon-reload
	sudo systemctl enable camera-dashboard
	@echo "Service installed. Start with: sudo systemctl start camera-dashboard"

# Run the application (kills any existing instance first)
run: build
	@echo "Stopping any existing instances..."
	-@pkill -9 -f "$(APP_NAME)" 2>/dev/null || true
	-@pkill -9 -f ffmpeg 2>/dev/null || true
	@sleep 0.5
	@echo "Starting $(APP_NAME)..."
	DISPLAY=:0 ./$(APP_NAME)

# Run with logging
run-log: build
	@echo "Stopping any existing instances..."
	-@pkill -9 -f "$(APP_NAME)" 2>/dev/null || true
	-@pkill -9 -f ffmpeg 2>/dev/null || true
	@sleep 0.5
	@echo "Starting $(APP_NAME) with logging..."
	DISPLAY=:0 ./$(APP_NAME) 2>&1 | tee camera.log

# Stop the application
stop:
	@echo "Stopping $(APP_NAME)..."
	-@pkill -9 -f "$(APP_NAME)" 2>/dev/null || true
	-@pkill -9 -f ffmpeg 2>/dev/null || true
	@echo "Stopped"

# Show status (CPU, memory, temperature)
status:
	@echo "=== Process Status ==="
	@ps aux | grep -E "$(APP_NAME)|ffmpeg" | grep -v grep || echo "Not running"
	@echo ""
	@echo "=== CPU & Memory ==="
	@ps aux | grep "$(APP_NAME)" | grep -v grep | awk '{print "CPU:", $$3"%, MEM:", $$6/1024"MB"}' || echo "Not running"
	@echo ""
	@echo "=== Temperature ==="
	@vcgencmd measure_temp 2>/dev/null || cat /sys/class/thermal/thermal_zone0/temp 2>/dev/null | awk '{print $$1/1000"C"}' || echo "N/A"
	@echo ""
	@echo "=== Zombie Processes ==="
	@ps aux | awk '$$8 == "Z" {count++} END {print count ? count " zombies" : "No zombies"}'

# Create deployment package (binary + installer)
package: release-optimized
	@echo "Creating deployment package..."
	@mkdir -p $(RELEASE_DIR)
	@cp install.sh $(RELEASE_DIR)/
	@chmod +x $(RELEASE_DIR)/install.sh
	@cd $(RELEASE_DIR) && tar -czvf $(APP_NAME)-$(VERSION)-linux-arm64.tar.gz $(APP_NAME) install.sh
	@echo ""
	@echo "Package created: $(RELEASE_DIR)/$(APP_NAME)-$(VERSION)-linux-arm64.tar.gz"
	@ls -lh $(RELEASE_DIR)/$(APP_NAME)-$(VERSION)-linux-arm64.tar.gz
	@echo ""
	@echo "To deploy to another Pi:"
	@echo "  scp $(RELEASE_DIR)/$(APP_NAME)-$(VERSION)-linux-arm64.tar.gz pi@<ip>:~/"
	@echo "  ssh pi@<ip> 'tar -xzvf $(APP_NAME)-*.tar.gz && ./install.sh'"

# Clean build artifacts
clean:
	@echo "Cleaning..."
	rm -f $(APP_NAME) $(APP_NAME)-debug
	rm -rf $(BUILD_DIR) $(RELEASE_DIR)
	go clean
	@echo "Clean complete"

# Show help
help:
	@echo "Camera Dashboard Build System"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Development:"
	@echo "  build          Build for current platform (dev)"
	@echo "  debug          Build with debug symbols"
	@echo "  run            Build and run (kills existing)"
	@echo "  run-log        Build and run with logging"
	@echo "  stop           Stop running instance"
	@echo "  status         Show CPU, memory, temperature"
	@echo "  clean          Remove build artifacts"
	@echo ""
	@echo "Release:"
	@echo "  release        Build optimized binary"
	@echo "  package        Create deployment tarball with installer"
	@echo ""
	@echo "Installation:"
	@echo "  install         Install to /usr/local/bin"
	@echo "  install-service Install systemd service for auto-start"
	@echo ""
	@echo "Configuration:"
	@echo "  Edit config.ini to change resolution/FPS/format"
	@echo ""
	@echo "Current settings:"
	@grep -E "capture_(width|height|fps|format)" config.ini 2>/dev/null | head -4 || echo "  (config.ini not found)"
