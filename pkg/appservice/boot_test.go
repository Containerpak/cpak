/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package appservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bootOptions(t *testing.T) BootOptions {
	t.Helper()
	root := t.TempDir()
	return BootOptions{
		Binary: filepath.Join(root, "bin", "cpak"), StoreDirectory: filepath.Join(root, "store", "services"),
		ConfigHome:     filepath.Join(root, "config"),
		SystemdRuntime: filepath.Join(root, "run", "systemd", "system"), Username: "user",
	}
}

func TestBootUsesCronWithoutSystemd(t *testing.T) {
	options := bootOptions(t)
	options.LookPath = func(name string) (string, error) {
		if name == "crontab" {
			return "/usr/bin/crontab", nil
		}
		return "", errors.New("missing")
	}
	var installed string
	options.Run = func(_ context.Context, name string, arguments []string, input []byte) ([]byte, error) {
		if name == "/usr/bin/crontab" && len(arguments) == 1 && arguments[0] == "-" {
			installed = string(input)
		}
		return nil, nil
	}
	record, err := ensureBoot(options)
	if err != nil {
		t.Fatal(err)
	}
	if record.Adapter != "cron" || !record.StartsBeforeLogin {
		t.Fatalf("boot record: %#v", record)
	}
	if !strings.Contains(installed, "service restore") {
		t.Fatalf("crontab: %q", installed)
	}
}

func TestBootFallsBackToCronWhenSystemdCannotStartBeforeLogin(t *testing.T) {
	options := bootOptions(t)
	if err := os.MkdirAll(options.SystemdRuntime, 0700); err != nil {
		t.Fatal(err)
	}
	options.LookPath = func(name string) (string, error) {
		switch name {
		case "systemctl", "loginctl", "crontab":
			return "/usr/bin/" + name, nil
		default:
			return "", errors.New("missing")
		}
	}
	systemdDisabled := false
	options.Run = func(_ context.Context, name string, arguments []string, _ []byte) ([]byte, error) {
		if name == "loginctl" {
			return []byte("not permitted"), errors.New("exit")
		}
		if name == "systemctl" && len(arguments) > 1 && arguments[1] == "disable" {
			systemdDisabled = true
		}
		return nil, nil
	}
	record, err := ensureBoot(options)
	if err != nil {
		t.Fatal(err)
	}
	if record.Adapter != "cron" || !record.StartsBeforeLogin {
		t.Fatalf("boot record: %#v", record)
	}
	if !systemdDisabled {
		t.Fatal("systemd remained enabled beside the cron adapter")
	}
}

func TestBootTreatsAMissingCrontabAsEmpty(t *testing.T) {
	options := bootOptions(t)
	options.LookPath = func(name string) (string, error) {
		if name == "crontab" {
			return "/usr/bin/crontab", nil
		}
		return "", errors.New("missing")
	}
	var installed string
	options.Run = func(_ context.Context, name string, arguments []string, input []byte) ([]byte, error) {
		if name != "/usr/bin/crontab" {
			return nil, nil
		}
		if len(arguments) == 1 && arguments[0] == "-l" {
			return []byte("no crontab for user\n"), errors.New("exit status 1")
		}
		installed = string(input)
		return nil, nil
	}
	record, err := ensureBoot(options)
	if err != nil {
		t.Fatal(err)
	}
	if record.Adapter != "cron" || strings.Contains(installed, "no crontab") {
		t.Fatalf("boot record: %#v, crontab: %q", record, installed)
	}
}

func TestBootRestoresSystemdWhenCronCannotBeInstalled(t *testing.T) {
	options := bootOptions(t)
	if err := os.MkdirAll(options.SystemdRuntime, 0700); err != nil {
		t.Fatal(err)
	}
	options.LookPath = func(name string) (string, error) {
		switch name {
		case "systemctl", "loginctl", "crontab":
			return "/usr/bin/" + name, nil
		default:
			return "", errors.New("missing")
		}
	}
	enabled := 0
	options.Run = func(_ context.Context, name string, arguments []string, _ []byte) ([]byte, error) {
		if name == "loginctl" {
			return []byte("not permitted"), errors.New("exit")
		}
		if name == "/usr/bin/crontab" {
			return []byte("permission denied"), errors.New("exit")
		}
		if name == "systemctl" && len(arguments) > 1 && arguments[1] == "enable" {
			enabled++
		}
		return nil, nil
	}
	record, err := ensureBoot(options)
	if err != nil {
		t.Fatal(err)
	}
	if record.Adapter != "systemd-user" || enabled != 2 || !strings.Contains(record.Warning, "cron fallback failed") {
		t.Fatalf("boot record: %#v, enable calls: %d", record, enabled)
	}
}

func TestBootUsesXDGAndWritesManualHookWithoutHostInit(t *testing.T) {
	options := bootOptions(t)
	options.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	options.Run = func(context.Context, string, []string, []byte) ([]byte, error) { return nil, nil }
	record, err := ensureBoot(options)
	if err != nil {
		t.Fatal(err)
	}
	if record.Adapter != "xdg-autostart" || record.StartsBeforeLogin || record.Warning == "" {
		t.Fatalf("boot record: %#v", record)
	}
	launcher := filepath.Join(options.ConfigHome, "cpak", "service-start")
	if info, statErr := os.Stat(launcher); statErr != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("manual launcher: %v, %#v", statErr, info)
	}
}

func TestBootLaunchersEscapeSpecifiers(t *testing.T) {
	if got := systemdQuote("/opt/cpak%test"); got != `"/opt/cpak%%test"` {
		t.Fatalf("systemd path: %s", got)
	}
	if got := desktopExecQuote("/opt/cpak%test"); got != `"/opt/cpak%%test"` {
		t.Fatalf("desktop path: %s", got)
	}
}
