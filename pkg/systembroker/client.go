/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
)

type Client struct {
	SocketPath string
	Token      string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
}

func (c Client) Notify(ctx context.Context, request NotificationRequest) error {
	return c.call(ctx, ActionNotify, request)
}

func (c Client) OpenURI(ctx context.Context, request OpenURIRequest) error {
	return c.call(ctx, ActionOpenURI, request)
}

func (c Client) LaunchApplication(ctx context.Context, request LaunchApplicationRequest) error {
	return c.call(ctx, ActionLaunchApplication, request)
}

func (c Client) FilePicker(ctx context.Context, request FilePickerRequest) (FilePickerResult, error) {
	var output bytes.Buffer
	client := c
	client.Stdout = &output
	if err := client.call(ctx, ActionFilePicker, request); err != nil {
		return FilePickerResult{}, err
	}
	var result FilePickerResult
	decoder := json.NewDecoder(&output)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return FilePickerResult{}, fmt.Errorf("decode file picker result: %w", err)
	}
	return result, nil
}

func (c Client) Containers(ctx context.Context, request ContainerRequest) error {
	return c.call(ctx, ActionContainers, request)
}

func (c Client) Cpak(ctx context.Context, request CpakRequest) error {
	return c.call(ctx, ActionCpak, request)
}

func (c Client) call(ctx context.Context, action string, payload any) error {
	if c.SocketPath == "" {
		return errors.New("system broker socket path is required")
	}
	if len(c.Token) < 32 {
		return errors.New("system broker token is too short")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode system broker request: %w", err)
	}
	connection, err := net.DialTimeout("unix", c.SocketPath, 3*time.Second)
	if err != nil {
		return fmt.Errorf("connect to system broker: %w", err)
	}
	defer connection.Close()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-done:
		}
	}()
	defer close(done)

	request := Request{Version: ProtocolVersion, Token: c.Token, Action: action, Payload: encoded}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("write system broker request: %w", err)
	}
	if c.Stdin != nil {
		go func() {
			_, _ = io.Copy(connection, c.Stdin)
			if unixConnection, ok := connection.(*net.UnixConn); ok {
				_ = unixConnection.CloseWrite()
			}
		}()
	}
	decoder := json.NewDecoder(connection)
	for {
		frame := Frame{}
		if err := decoder.Decode(&frame); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read system broker response: %w", err)
		}
		switch frame.Type {
		case FrameStdout:
			if c.Stdout != nil {
				if _, err := c.Stdout.Write(frame.Data); err != nil {
					return err
				}
			}
		case FrameStderr:
			if c.Stderr != nil {
				if _, err := c.Stderr.Write(frame.Data); err != nil {
					return err
				}
			}
		case FrameError:
			if frame.Error == "" {
				return errors.New("system broker request failed")
			}
			return errors.New(frame.Error)
		case FrameExit:
			if frame.ExitCode != 0 {
				return &types.ExitError{Code: frame.ExitCode}
			}
			return nil
		default:
			return errors.New("invalid system broker response")
		}
	}
}
