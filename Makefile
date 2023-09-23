.PHONY: all clean download cpak
all: clean download cpak

clean:
	@rm -f cpak
	@rm -f pkg/tools/rootlesskit.tar.gz
	@rm -f pkg/tools/host-spawn

download:
	curl -L \
		https://github.com/rootless-containers/rootlesskit/releases/download/v2.0.0-alpha.0/rootlesskit-x86_64.tar.gz \
		-o pkg/tools/rootlesskit.tar.gz
	curl -L \
		https://github.com/1player/host-spawn/releases/download/1.5.0/host-spawn-x86_64 \
		-o pkg/tools/host-spawn

cpak:
	go build -o cpak .
	