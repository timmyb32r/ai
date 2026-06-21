.PHONY: install uninstall build-image build-base build-client

IMAGE_NAME := timmy-code
INSTALL_PATH := /usr/local/bin/timmy
CLIENT_INSTALL_PATH := /usr/local/bin/timmy-client
TIMKY_HOME := $(shell pwd)

build-base:
	docker build -f Dockerfile.base -t timmy-code-base .

build-image: build-base
	docker build --build-arg COMMIT_HASH=$(shell git rev-parse HEAD 2>/dev/null || echo "unknown") -t $(IMAGE_NAME) .

build-client:
	@echo "Building timmy-client for host..."
	go build -o bin/timmy-client ./cmd/timmy-client/
	@echo "Client built: bin/timmy-client"

install: build-base build-image build-client
	@echo "Installing timmy to $(INSTALL_PATH)..."
	@sed 's|__TIMKY_HOME__|$(TIMKY_HOME)|g' scripts/timmy.sh | sudo tee $(INSTALL_PATH) > /dev/null
	sudo chmod +x $(INSTALL_PATH)
	@echo "Installing timmy-client to $(CLIENT_INSTALL_PATH)..."
	sudo cp bin/timmy-client $(CLIENT_INSTALL_PATH)
	sudo chmod +x $(CLIENT_INSTALL_PATH)
	@echo "Done! Run: timmy \"hello\""

uninstall:
	sudo rm -f $(INSTALL_PATH)
	sudo rm -f $(CLIENT_INSTALL_PATH)
	docker rmi $(IMAGE_NAME) 2>/dev/null || true
	docker rmi timmy-code-base 2>/dev/null || true
	@echo "Uninstalled."
