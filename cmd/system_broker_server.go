/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/systembroker"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type SystemBrokerServerCmd struct {
	SocketPath string `cli:"socket-path" help:"Path for the Unix domain socket"`
	TokenFile  string `cli:"token-file" help:"File containing the broker token"`
	Notify     bool   `cli:"notify" help:"Allow desktop notification requests"`
	OpenURI    bool   `cli:"open-uri" help:"Allow external URI requests"`

	cli.Base
}

func (c *SystemBrokerServerCmd) Run() error {
	token, err := readSystemBrokerToken(c.TokenFile)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	return systembroker.Serve(ctx, systembroker.Options{
		SocketPath:   c.SocketPath,
		Token:        token,
		AllowNotify:  c.Notify,
		AllowOpenURI: c.OpenURI,
	})
}

func readSystemBrokerToken(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("system broker token file is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read system broker token: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("system broker token file must not be accessible by other users")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read system broker token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 {
		return "", fmt.Errorf("system broker token file is invalid")
	}
	return token, nil
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-signals:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(signals)
	}()
	return ctx, cancel
}
