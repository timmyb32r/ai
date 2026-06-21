.PHONY: install uninstall build-image build-base

IMAGE_NAME := timmy-code
INSTALL_PATH := /usr/local/bin/timmy
TIMKY_HOME := $(shell pwd)

build-base:
	docker build -f Dockerfile.base -t timmy-code-base .

build-image: build-base
	docker build --build-arg COMMIT_HASH=$(shell git rev-parse HEAD 2>/dev/null || echo "unknown") -t $(IMAGE_NAME) .

install: build-base build-image
	@echo "Installing timmy to $(INSTALL_PATH)..."
	@sed 's|__TIMKY_HOME__|$(TIMKY_HOME)|g' scripts/timmy.sh | sudo tee $(INSTALL_PATH) > /dev/null
	sudo chmod +x $(INSTALL_PATH)
	@echo "Done! Run: timmy \"hello\""

uninstall:
	sudo rm -f $(INSTALL_PATH)
	docker rmi $(IMAGE_NAME) 2>/dev/null || true
	docker rmi timmy-code-base 2>/dev/null || true
	@echo "Uninstalled."
