.PHONY: all clean cpak
all: clean cpak

clean:
	@rm -f cpak

cpak:
	go build -trimpath -ldflags="-s -w" -o cpak .
