.PHONY: all clean cpak storaged installer ui-adapters

VERSION ?= dev
SELF_UPDATE_MODE ?= enabled
DIALOG_BACKEND ?= auto
UI_ADAPTERS ?= all
UI_ADAPTER_BUILD_DIR ?= .build/ui-adapters
comma := ,
UI_ADAPTER_TAGS := $(if $(filter all,$(UI_ADAPTERS)),,$(foreach adapter,$(subst $(comma), ,$(UI_ADAPTERS)),cpak_ui_$(adapter)))
GO_UI_ADAPTER_TAGS := $(if $(UI_ADAPTER_TAGS),-tags "$(UI_ADAPTER_TAGS)",)

all: clean cpak

clean:
	@rm -f cpak cpak-storaged cpak-installer cpak-sign
	@rm -f pkg/desktopui/adapter_embedded_*_generated.go
	@rm -rf $(UI_ADAPTER_BUILD_DIR)

ui-adapters:
	@rm -f pkg/desktopui/adapter_embedded_*_generated.go
	@rm -rf $(UI_ADAPTER_BUILD_DIR)
	@mkdir -p $(UI_ADAPTER_BUILD_DIR)
	@set -eu; \
	selected=",$(UI_ADAPTERS),"; \
	if { printf '%s' "$$selected" | grep -q ',all,' || printf '%s' "$$selected" | grep -q ',builtin,'; } && [ "$(UI_ADAPTERS)" != "all" ] && [ "$(UI_ADAPTERS)" != "builtin" ]; then \
		echo "all and builtin cannot be combined with another UI adapter" >&2; exit 2; \
	fi; \
	for adapter in $(subst $(comma), ,$(UI_ADAPTERS)); do \
		case "$$adapter" in all|builtin|adwaita|gtk|kde|qt) ;; *) echo "unsupported UI adapter: $$adapter" >&2; exit 2 ;; esac; \
	done; \
	args=""; \
	if [ "$(UI_ADAPTERS)" = "all" ] || printf '%s' "$$selected" | grep -q ',gtk,'; then \
		cc -O2 -Wall -Wextra -Werror ui/adapters/gtk/main.c -o $(UI_ADAPTER_BUILD_DIR)/cpak-ui-gtk $$(pkg-config --cflags --libs gtk+-3.0); \
		args="$$args --gtk $(UI_ADAPTER_BUILD_DIR)/cpak-ui-gtk"; \
	fi; \
	if [ "$(UI_ADAPTERS)" = "all" ] || printf '%s' "$$selected" | grep -q ',adwaita,'; then \
		cc -O2 -Wall -Wextra -Werror -Wno-deprecated-declarations ui/adapters/adwaita/main.c -o $(UI_ADAPTER_BUILD_DIR)/cpak-ui-adwaita $$(pkg-config --cflags --libs libadwaita-1); \
		args="$$args --adwaita $(UI_ADAPTER_BUILD_DIR)/cpak-ui-adwaita"; \
	fi; \
	if [ "$(UI_ADAPTERS)" = "all" ] || printf '%s' "$$selected" | grep -q ',qt,'; then \
		c++ -O2 -Wall -Wextra -Werror -fPIC -DCPAK_UI_BACKEND='"qt"' ui/adapters/qt/main.cpp -o $(UI_ADAPTER_BUILD_DIR)/cpak-ui-qt $$(pkg-config --cflags --libs Qt6Widgets); \
		args="$$args --qt $(UI_ADAPTER_BUILD_DIR)/cpak-ui-qt"; \
	fi; \
	if [ "$(UI_ADAPTERS)" = "all" ] || printf '%s' "$$selected" | grep -q ',kde,'; then \
		c++ -O2 -Wall -Wextra -Werror -fPIC -DCPAK_UI_BACKEND='"kde"' ui/adapters/qt/main.cpp -o $(UI_ADAPTER_BUILD_DIR)/cpak-ui-kde $$(pkg-config --cflags --libs Qt6Widgets); \
		args="$$args --kde $(UI_ADAPTER_BUILD_DIR)/cpak-ui-kde"; \
	fi; \
	go run ./cmd/cpak-ui-bundle --output pkg/desktopui $$args

cpak: ui-adapters
	CGO_ENABLED=0 go build $(GO_UI_ADAPTER_TAGS) -trimpath -ldflags="-s -w -X main.version=$(VERSION) -X main.selfUpdateMode=$(SELF_UPDATE_MODE) -X github.com/mirkobrombin/cpak/pkg/desktopui.defaultBackend=$(DIALOG_BACKEND)" -o cpak .

storaged:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o cpak-storaged ./cmd/cpak-storaged

sign:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o cpak-sign ./cmd/cpak-sign

installer: cpak storaged
	CGO_ENABLED=0 go build $(GO_UI_ADAPTER_TAGS) -trimpath -ldflags="-s -w -X github.com/mirkobrombin/cpak/pkg/desktopui.defaultBackend=$(DIALOG_BACKEND)" -o /tmp/cpak-installer ./cmd/cpak-installer
	go run ./cmd/cpak-pack --installer /tmp/cpak-installer --cpak cpak --storaged cpak-storaged --brand-icon cpak-icon.png --output cpak-installer
