package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	storage "github.com/containerpak/storage/pkg/driver"
	"github.com/mirkobrombin/cpak/pkg/storaged"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var socket string
	var control string
	var sourceRoot string
	var driverRoot string
	var name string
	flag.StringVar(&socket, "socket", "", "Unix socket path")
	flag.StringVar(&control, "control", "", "Compatibility control address")
	flag.StringVar(&sourceRoot, "source-root", "", "FVS source root")
	flag.StringVar(&driverRoot, "driver-root", "", "Driver data root")
	flag.StringVar(&name, "driver", "fvs", "Storage driver")
	flag.Parse()
	if socket == "" && strings.HasPrefix(control, "unix:") {
		socket = strings.TrimPrefix(control, "unix:")
	}
	if socket == "" || sourceRoot == "" || driverRoot == "" {
		return errors.New("socket, source-root and driver-root are required")
	}
	var handler storage.Handler
	var err error
	switch name {
	case "fvs":
		handler, err = storaged.NewFVS(sourceRoot, driverRoot)
	case "dabadee":
		handler, err = storaged.NewDaBaDee(sourceRoot, driverRoot)
	default:
		return fmt.Errorf("unsupported storage driver %q", name)
	}
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := storage.Server{SocketPath: socket, Handler: handler}
	return server.Serve(ctx)
}
