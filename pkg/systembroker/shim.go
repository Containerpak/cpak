/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/term"
)

func InvokeShim(ctx context.Context, socketPath, token, shim string, args []string, environment map[string]string, stdin io.Reader, stdout, stderr io.Writer, interactive bool) error {
	client := Client{SocketPath: socketPath, Token: token, Stdin: stdin, Stdout: stdout, Stderr: stderr}
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
		request.ActivationToken = environment["XDG_ACTIVATION_TOKEN"]
		return client.OpenURI(ctx, request)
	case "gio":
		request, err := parseGIOOpen(args)
		if err != nil {
			return err
		}
		request.ActivationToken = environment["XDG_ACTIVATION_TOKEN"]
		return client.OpenURI(ctx, request)
	case "cpak-launch-app":
		request, err := parseApplication(args, environment)
		if err != nil {
			return err
		}
		return client.LaunchApplication(ctx, request)
	case "cpak-file-picker":
		request, err := parseFilePicker(args)
		if err != nil {
			return err
		}
		result, err := client.FilePicker(ctx, request)
		if err != nil {
			return err
		}
		paths := result.Paths
		if len(paths) == 0 {
			paths = []string{result.Path}
		}
		for _, path := range paths {
			if _, err = fmt.Fprintln(stdout, path); err != nil {
				return err
			}
		}
		return nil
	case "podman", "docker":
		request, err := parseContainerCommand(args)
		if err != nil {
			return err
		}
		if shim == "docker" {
			request.Backend = "docker"
		}
		return client.Containers(ctx, request)
	case "cpak-host":
		rows, columns := shimTerminalSize(stdin, interactive)
		return client.Cpak(ctx, CpakRequest{
			Arguments:   append([]string{}, args...),
			Interactive: interactive,
			Rows:        rows,
			Columns:     columns,
		})
	default:
		return errors.New("unsupported system integration shim")
	}
}

func shimTerminalSize(input io.Reader, interactive bool) (uint16, uint16) {
	file, ok := input.(interface{ Fd() uintptr })
	if !interactive || !ok {
		return 0, 0
	}
	columns, rows, err := term.GetSize(int(file.Fd()))
	if err != nil || rows <= 0 || columns <= 0 {
		return 0, 0
	}
	if rows > 1<<16-1 {
		rows = 1<<16 - 1
	}
	if columns > 1<<16-1 {
		columns = 1<<16 - 1
	}
	return uint16(rows), uint16(columns)
}

func parseFilePicker(args []string) (FilePickerRequest, error) {
	if len(args) == 0 {
		return FilePickerRequest{}, errors.New("file picker mode is required")
	}
	request := FilePickerRequest{Mode: args[0], Title: "Select a file"}
	for index := 1; index < len(args); index++ {
		name := args[index]
		if name == "--multiple" {
			request.Multiple = true
			continue
		}
		if name != "--title" && name != "--accept-label" && name != "--suggested-name" && name != "--current-folder" && name != "--filter" && name != "--mime-filter" {
			return FilePickerRequest{}, fmt.Errorf("unsupported file picker option: %s", name)
		}
		index++
		if index >= len(args) {
			return FilePickerRequest{}, fmt.Errorf("file picker option %s requires a value", name)
		}
		value := args[index]
		switch name {
		case "--title":
			request.Title = value
		case "--accept-label":
			request.AcceptLabel = value
		case "--suggested-name":
			request.SuggestedName = value
		case "--current-folder":
			request.CurrentFolder = value
		case "--filter":
			parts := strings.SplitN(value, "|", 2)
			if len(parts) != 2 {
				return FilePickerRequest{}, errors.New("file picker filter must use NAME|PATTERN;PATTERN")
			}
			request.Filters = append(request.Filters, FilePickerFilter{Name: parts[0], Patterns: strings.Split(parts[1], ";")})
		case "--mime-filter":
			parts := strings.SplitN(value, "|", 2)
			if len(parts) != 2 {
				return FilePickerRequest{}, errors.New("file picker MIME filter must use NAME|TYPE;TYPE")
			}
			request.Filters = append(request.Filters, FilePickerFilter{Name: parts[0], MIMETypes: strings.Split(parts[1], ";")})
		}
	}
	return request, nil
}

func parseGIOOpen(args []string) (OpenURIRequest, error) {
	if len(args) != 2 || args[0] != "open" {
		return OpenURIRequest{}, errors.New("gio broker access is limited to one URI passed to gio open")
	}
	return parseOpenURI(args[1:])
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
