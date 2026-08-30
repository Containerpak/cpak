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
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

const maxCpakArgumentsSize = 64 << 10

func executeCpak(ctx context.Context, capabilities map[string]bool, request CpakRequest, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	arguments, err := validateCpakRequest(capabilities, request)
	if err != nil {
		return 0, err
	}
	binary, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate cpak executable: %w", err)
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return 0, fmt.Errorf("resolve cpak executable: %w", err)
	}
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err = command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 0, fmt.Errorf("run cpak host action: %w", err)
	}
	return 0, nil
}

func validateCpakRequest(capabilities map[string]bool, request CpakRequest) ([]string, error) {
	arguments := request.Arguments
	if len(arguments) < 2 || len(arguments) > 64 {
		return nil, errors.New("invalid cpak host action")
	}
	total := 0
	for _, argument := range arguments {
		total += len(argument)
		if argument == "" || strings.ContainsAny(argument, "\x00\r\n") {
			return nil, errors.New("invalid cpak host action")
		}
	}
	if total > maxCpakArgumentsSize {
		return nil, errors.New("invalid cpak host action")
	}

	var capability string
	switch arguments[0] {
	case "discover":
		if request.Interactive {
			return nil, errors.New("cpak discovery is not interactive")
		}
		switch arguments[1] {
		case "list":
			if len(arguments) != 2 {
				return nil, errors.New("invalid cpak discovery request")
			}
			capability = types.HostActionCpakRead
		case "install":
			if len(arguments) != 3 || !validCpakValue(arguments[2], 512) {
				return nil, errors.New("invalid cpak discovery request")
			}
			capability = types.HostActionCpakManage
		default:
			return nil, errors.New("unsupported cpak discovery request")
		}
	case "environment":
		var err error
		capability, err = validateCpakEnvironmentArguments(arguments, request.Interactive)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("unsupported cpak host action")
	}
	if !capabilities[capability] {
		return nil, fmt.Errorf("cpak %s actions are not permitted", capability)
	}
	return append([]string{}, arguments...), nil
}

func validateCpakEnvironmentArguments(arguments []string, interactive bool) (string, error) {
	switch arguments[1] {
	case "list":
		if interactive || !equalArguments(arguments[2:], "--json") {
			return "", errors.New("invalid cpak environment request")
		}
		return types.HostActionCpakRead, nil
	case "permissions", "processes":
		if interactive || len(arguments) != 5 || arguments[2] != "--environment" || !validCpakValue(arguments[3], 160) || arguments[4] != "--json" {
			return "", errors.New("invalid cpak environment request")
		}
		return types.HostActionCpakRead, nil
	case "signals":
		if interactive || !equalArguments(arguments[2:], "--json") {
			return "", errors.New("invalid cpak environment request")
		}
		return types.HostActionCpakRead, nil
	case "create":
		if interactive || len(arguments) != 7 || arguments[2] != "--name" || !validCpakValue(arguments[3], 80) || arguments[4] != "--origin" || !validCpakValue(arguments[5], 512) || arguments[6] != "--json" {
			return "", errors.New("invalid cpak environment request")
		}
		return types.HostActionCpakManage, nil
	case "policy":
		if interactive || len(arguments) != 7 || arguments[2] != "--environment" || !validCpakValue(arguments[3], 160) || arguments[4] != "--policy" || arguments[5] != "-" || arguments[6] != "--json" {
			return "", errors.New("invalid cpak environment request")
		}
		return types.HostActionCpakManage, nil
	case "stop", "delete":
		if interactive || len(arguments) != 4 || arguments[2] != "--environment" || !validCpakValue(arguments[3], 160) {
			return "", errors.New("invalid cpak environment request")
		}
		return types.HostActionCpakManage, nil
	case "signal":
		if interactive || len(arguments) != 8 || arguments[2] != "--environment" || !validCpakValue(arguments[3], 160) || arguments[4] != "--pid" || arguments[6] != "--signal" || !validCpakValue(arguments[7], 32) {
			return "", errors.New("invalid cpak environment request")
		}
		pid, err := strconv.Atoi(arguments[5])
		if err != nil || pid <= 0 {
			return "", errors.New("invalid cpak environment process")
		}
		return types.HostActionCpakManage, nil
	case "shell":
		if !interactive || len(arguments) < 6 || arguments[2] != "--environment" || !validCpakValue(arguments[3], 160) || arguments[4] != "--command" || !validCpakValue(arguments[5], 4096) {
			return "", errors.New("invalid cpak environment request")
		}
		return types.HostActionCpakExec, nil
	default:
		return "", errors.New("unsupported cpak environment request")
	}
}

func validCpakValue(value string, limit int) bool {
	return value != "" && len(value) <= limit && !strings.HasPrefix(value, "-") && !strings.ContainsAny(value, "\x00\r\n")
}

func equalArguments(arguments []string, expected ...string) bool {
	if len(arguments) != len(expected) {
		return false
	}
	for index := range arguments {
		if arguments[index] != expected[index] {
			return false
		}
	}
	return true
}
