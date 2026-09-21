.PHONY: build release clean

# Build variables
BINARY_NAME=gotunnel
RELEASE_DIR=release
GO=go
GOOS?=$(shell go env GOOS)
GOARCH?=$(shell go env GOARCH)
VERSION=$(shell cat version.txt)

# Build the binary
build:
	@echo "Building $(BINARY_NAME) v$(VERSION)..."
	@mkdir -p $(RELEASE_DIR)
	$(GO) build -ldflags="-X github.com/yoyo/gotunnel/internal/config.Version=v$(VERSION)" -o $(RELEASE_DIR)/$(BINARY_NAME) .

# Create a release zip file
release: clean build
	@echo "Creating release archive..."
	@cd $(RELEASE_DIR) && zip -r $(BINARY_NAME)-$(GOOS)-$(GOARCH).zip $(BINARY_NAME)
	@echo "Release archive created: $(RELEASE_DIR)/$(BINARY_NAME)-$(GOOS)-$(GOARCH).zip"

# Clean the release directory (keeps .gitkeep)
clean:
	@echo "Cleaning..."
	@rm -f $(RELEASE_DIR)/$(BINARY_NAME) $(RELEASE_DIR)/*.zip

# Help target
help:
	@echo "Available targets:"
	@echo "  make build       - Build the binary into the release folder"
	@echo "  make release     - Build and create a zip archive for GitHub releases"
	@echo "  make clean       - Clean the release directory"
	@echo "  make help        - Show this help message"
