.PHONY: all clean download cpak
all: clean download cpak

clean:
	@rm -f cpak
	@rm -f pkg/tools/rootlesskit.tar.gz

download:
	curl -L \
		https://github.com/rootless-containers/rootlesskit/releases/download/v2.0.0-alpha.0/rootlesskit-x86_64.tar.gz \
		-o pkg/tools/rootlesskit.tar.gz

cpak:
	go build -o cpak .
	