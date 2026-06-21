.PHONY: install uninstall build-image

IMAGE_NAME := timmy-code
INSTALL_PATH := /usr/local/bin/timmy

build-image:
	docker build -t $(IMAGE_NAME) .

install: build-image
	@echo "Installing timmy to $(INSTALL_PATH)..."
	sudo cp scripts/timmy.sh $(INSTALL_PATH)
	sudo chmod +x $(INSTALL_PATH)
	@echo "Done! Run: timmy \"hello\""

uninstall:
	sudo rm -f $(INSTALL_PATH)
	docker rmi $(IMAGE_NAME) 2>/dev/null || true
	@echo "Uninstalled."
