.PHONY: all clean download nsenter cpak
all: clean download nsenter cpak

clean:
	@rm -f cpak
	@rm -f pkg/tools/rootlesskit.tar.gz
	@rm -f pkg/tools/nsenter

download:
	curl -L \
		https://github.com/rootless-containers/rootlesskit/releases/download/v2.3.5/rootlesskit-x86_64.tar.gz \
		-o pkg/tools/rootlesskit.tar.gz

nsenter:
	musl-gcc -static -O2 -s native/nsenter/nsenter.c -o pkg/tools/nsenter

cpak:
	go build -trimpath -ldflags="-s -w" -o cpak .
