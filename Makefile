.PHONY: all clean cpak installer

VERSION ?= dev

all: clean cpak

clean:
	@rm -f cpak cpak-installer

cpak:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o cpak .

installer: cpak
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /tmp/cpak-installer ./cmd/cpak-installer
	go run ./cmd/cpak-pack --installer /tmp/cpak-installer --cpak cpak --output cpak-installer
