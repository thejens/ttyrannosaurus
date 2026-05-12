XTERM_VERSION       = 5.3.0
FIT_ADDON_VERSION   = 0.10.0
WEBGL_ADDON_VERSION = 0.18.0
LINKS_ADDON_VERSION = 0.12.0

XTERM_CDN  = https://cdn.jsdelivr.net/npm/xterm@$(XTERM_VERSION)/lib
FIT_CDN    = https://cdn.jsdelivr.net/npm/@xterm/addon-fit@$(FIT_ADDON_VERSION)/lib
WEBGL_CDN  = https://cdn.jsdelivr.net/npm/@xterm/addon-webgl@$(WEBGL_ADDON_VERSION)/lib
LINKS_CDN  = https://cdn.jsdelivr.net/npm/@xterm/addon-web-links@$(LINKS_ADDON_VERSION)/lib
XTERM_CSS  = https://cdn.jsdelivr.net/npm/xterm@$(XTERM_VERSION)/css/xterm.css

STATIC_DIR = daemon/static
BIN        = bin/ttyrannosaurus
LAUNCH_PLIST = $(HOME)/Library/LaunchAgents/com.ttyrannosaurus.daemon.plist

.PHONY: all build static-assets daemon url-handler install uninstall clean help

all: build

help:
	@echo "Targets:"
	@echo "  build          Build daemon binary"
	@echo "  static-assets  Download xterm.js assets"
	@echo "  url-handler    Build macOS URL handler .app"
	@echo "  install        Build and install everything"
	@echo "  uninstall      Remove installed files"
	@echo "  clean          Remove build artifacts"

static-assets:
	@mkdir -p $(STATIC_DIR)
	@echo "Downloading xterm.js $(XTERM_VERSION)..."
	@curl -fsSL $(XTERM_CDN)/xterm.js        -o $(STATIC_DIR)/xterm.js
	@curl -fsSL $(XTERM_CDN)/xterm.js.map    -o $(STATIC_DIR)/xterm.js.map
	@curl -fsSL $(XTERM_CSS)                 -o $(STATIC_DIR)/xterm.css
	@echo "Downloading @xterm/addon-fit $(FIT_ADDON_VERSION)..."
	@curl -fsSL $(FIT_CDN)/addon-fit.js      -o $(STATIC_DIR)/xterm-addon-fit.js
	@echo "Downloading @xterm/addon-webgl $(WEBGL_ADDON_VERSION)..."
	@curl -fsSL $(WEBGL_CDN)/addon-webgl.js   -o $(STATIC_DIR)/xterm-addon-webgl.js
	@echo "Downloading @xterm/addon-web-links $(LINKS_ADDON_VERSION)..."
	@curl -fsSL $(LINKS_CDN)/addon-web-links.js -o $(STATIC_DIR)/xterm-addon-web-links.js
	@echo "Static assets ready."

daemon: static-assets
	@mkdir -p bin
	go build -o $(BIN) ./daemon

build: daemon

url-handler:
	@cd url-handler && bash build.sh

install: daemon url-handler
	@echo "Installing daemon..."
	@cp $(BIN) /usr/local/bin/ttyrannosaurus
	@echo "Installing URL handler..."
	@cp -r url-handler/build/ttyrannosaurus-url-handler.app /Applications/
	@/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister \
		-f /Applications/ttyrannosaurus-url-handler.app
	@echo "Installing launchd agent..."
	@mkdir -p $(HOME)/Library/LaunchAgents
	@cp daemon/com.ttyrannosaurus.daemon.plist $(LAUNCH_PLIST)
	@launchctl unload $(LAUNCH_PLIST) 2>/dev/null || true
	@launchctl load $(LAUNCH_PLIST)
	@echo ""
	@echo "ttyrannosaurus installed. Daemon running on http://localhost:7071"
	@echo "Test URL scheme: open ttyrannosaurus://claude/new"

uninstall:
	@launchctl unload $(LAUNCH_PLIST) 2>/dev/null || true
	@rm -f $(LAUNCH_PLIST)
	@rm -f /usr/local/bin/ttyrannosaurus
	@rm -rf /Applications/ttyrannosaurus-url-handler.app
	@echo "ttyrannosaurus uninstalled."

clean:
	@rm -rf bin/
	@rm -f $(STATIC_DIR)/xterm*.js $(STATIC_DIR)/xterm*.map $(STATIC_DIR)/xterm*.css
	@rm -rf url-handler/build/
