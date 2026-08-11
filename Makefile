.PHONY: all clean cpak

VERSION ?= dev

all: clean cpak

clean:
	@rm -f cpak

cpak:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o cpak .
