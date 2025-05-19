.PHONY: all clean download cpak
all: clean download cpak

clean:
	@rm -f cpak
	@rm -f pkg/tools/rootlesskit.tar.gz
	@rm -f pkg/tools/busybox

download:
	curl -L \
		https://github.com/rootless-containers/rootlesskit/releases/download/v2.0.0-alpha.0/rootlesskit-x86_64.tar.gz \
		-o pkg/tools/rootlesskit.tar.gz
	curl -L \
		https://busybox.net/downloads/binaries/1.35.0-x86_64-linux-musl/busybox \
		-o pkg/tools/busybox

cpak:
	go build -trimpath -ldflags="-s -w" -o cpak .
