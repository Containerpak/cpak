.PHONY: all clean cpak installer

VERSION ?= dev
SELF_UPDATE_MODE ?= enabled
DIALOG_BACKEND ?= auto

all: clean cpak

clean:
	@rm -f cpak cpak-installer

cpak:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION) -X main.selfUpdateMode=$(SELF_UPDATE_MODE) -X github.com/mirkobrombin/cpak/pkg/desktopui.defaultBackend=$(DIALOG_BACKEND)" -o cpak .

installer: cpak
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/mirkobrombin/cpak/pkg/desktopui.defaultBackend=$(DIALOG_BACKEND)" -o /tmp/cpak-installer ./cmd/cpak-installer
	go run ./cmd/cpak-pack --installer /tmp/cpak-installer --cpak cpak --output cpak-installer
