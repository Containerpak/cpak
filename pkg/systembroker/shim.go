/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systembroker

import (
	"context"
	"errors"
	"io"
)

func InvokeShim(ctx context.Context, socketPath, token, shim string, args []string, environment map[string]string, stdout, stderr io.Writer) error {
	client := Client{SocketPath: socketPath, Token: token, Stdout: stdout, Stderr: stderr}
	switch shim {
	case "notify-send":
		request, err := parseNotification(args)
		if err != nil {
			return err
		}
		return client.Notify(ctx, request)
	case "xdg-open":
		request, err := parseOpenURI(args)
		if err != nil {
			return err
		}
		return client.OpenURI(ctx, request)
	case "cpak-launch-app":
		request, err := parseApplication(args, environment)
		if err != nil {
			return err
		}
		return client.LaunchApplication(ctx, request)
	case "podman", "docker":
		request, err := parseContainerCommand(args)
		if err != nil {
			return err
		}
		return client.Containers(ctx, request)
	default:
		return errors.New("unsupported system integration shim")
	}
}

func parseOpenURI(args []string) (OpenURIRequest, error) {
	if len(args) != 1 {
		return OpenURIRequest{}, errors.New("opening a URI requires one argument")
	}
	request := OpenURIRequest{URI: args[0]}
	if err := validateOpenURI(request); err != nil {
		return OpenURIRequest{}, err
	}
	return request, nil
}

func parseApplication(args []string, environment map[string]string) (LaunchApplicationRequest, error) {
	if len(args) == 0 {
		return LaunchApplicationRequest{}, errors.New("host application token is required")
	}
	request := LaunchApplicationRequest{
		ApplicationToken: args[0],
		URIs:             append([]string{}, args[1:]...),
		Environment:      environment,
	}
	return request, nil
}
