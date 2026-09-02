/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package appservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

type BootRecord struct {
	Adapter           string `json:"adapter"`
	Path              string `json:"path"`
	StartsBeforeLogin bool   `json:"starts_before_login"`
	Warning           string `json:"warning,omitempty"`
}

type BootOptions struct {
	Binary         string
	StoreDirectory string
	ConfigHome     string
	SystemdRuntime string
	Username       string
	LookPath       func(string) (string, error)
	Run            func(context.Context, string, []string, []byte) ([]byte, error)
}

func EnsureBoot(binary, storeDirectory string) (BootRecord, error) {
	configHome, err := os.UserConfigDir()
	if err != nil {
		return BootRecord{}, err
	}
	username := ""
	if current, currentErr := user.Current(); currentErr == nil {
		username = current.Username
	}
	options := BootOptions{
		Binary: binary, StoreDirectory: storeDirectory,
		ConfigHome: configHome, SystemdRuntime: "/run/systemd/system",
		Username: username, LookPath: exec.LookPath, Run: runBootCommand,
	}
	return ensureBoot(options)
}

func ensureBoot(options BootOptions) (BootRecord, error) {
	if !filepath.IsAbs(options.Binary) || !filepath.IsAbs(options.StoreDirectory) || !filepath.IsAbs(options.ConfigHome) {
		return BootRecord{}, errors.New("boot activation paths must be absolute")
	}
	if options.LookPath == nil || options.Run == nil {
		return BootRecord{}, errors.New("boot activation command hooks are missing")
	}
	manual, err := writeManualLauncher(options)
	if err != nil {
		return BootRecord{}, err
	}
	var loginFallback *BootRecord
	if info, statErr := os.Stat(options.SystemdRuntime); statErr == nil && info.IsDir() {
		if _, pathErr := options.LookPath("systemctl"); pathErr == nil {
			record, systemdErr := installSystemdUser(options)
			if systemdErr == nil {
				if record.StartsBeforeLogin {
					return saveBootRecord(options.StoreDirectory, record)
				}
				loginFallback = &record
			}
		}
	}
	if crontab, pathErr := options.LookPath("crontab"); pathErr == nil {
		systemdDisabled := false
		if loginFallback != nil {
			if disableErr := setSystemdUserEnabled(options, false); disableErr != nil {
				loginFallback.Warning += "; cron fallback was not installed: " + disableErr.Error()
				return saveBootRecord(options.StoreDirectory, *loginFallback)
			}
			systemdDisabled = true
		}
		record, cronErr := installCron(options, crontab)
		if cronErr == nil {
			return saveBootRecord(options.StoreDirectory, record)
		}
		if systemdDisabled {
			if enableErr := setSystemdUserEnabled(options, true); enableErr != nil {
				return BootRecord{}, errors.Join(cronErr, enableErr)
			}
			loginFallback.Warning += "; cron fallback failed: " + cronErr.Error()
		}
	}
	if loginFallback != nil {
		return saveBootRecord(options.StoreDirectory, *loginFallback)
	}
	record, err := installXDGAutostart(options)
	if err != nil {
		return BootRecord{}, err
	}
	record.Warning = "starts at graphical login; add " + manual + " to the host init for pre-login startup"
	return saveBootRecord(options.StoreDirectory, record)
}

func installSystemdUser(options BootOptions) (BootRecord, error) {
	path := filepath.Join(options.ConfigHome, "systemd", "user", "cpak.service")
	unit := "[Unit]\nDescription=cpak service manager\nAfter=network-online.target\nWants=network-online.target\n\n" +
		"[Service]\nExecStart=" + systemdQuote(options.Binary) + " service restore\nRestart=on-failure\nRestartSec=2\n\n" +
		"[Install]\nWantedBy=default.target\n"
	if err := writeFile(path, []byte(unit), 0644); err != nil {
		return BootRecord{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := options.Run(ctx, "systemctl", []string{"--user", "daemon-reload"}, nil); err != nil {
		return BootRecord{}, fmt.Errorf("reload user services: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := setSystemdUserEnabled(options, true); err != nil {
		return BootRecord{}, err
	}
	record := BootRecord{Adapter: "systemd-user", Path: path}
	if options.Username == "" {
		record.Warning = "the user manager starts at login; enable lingering for pre-login startup"
		return record, nil
	}
	linger := filepath.Join("/var/lib/systemd/linger", options.Username)
	if _, err := os.Stat(linger); err == nil {
		record.StartsBeforeLogin = true
		return record, nil
	}
	if _, err := options.LookPath("loginctl"); err != nil {
		record.Warning = "the user manager starts at login; enable lingering for pre-login startup"
		return record, nil
	}
	output, err := options.Run(ctx, "loginctl", []string{"enable-linger", options.Username}, nil)
	if err == nil {
		record.StartsBeforeLogin = true
		return record, nil
	}
	record.Warning = "the user manager starts at login; loginctl enable-linger failed: " + strings.TrimSpace(string(output))
	return record, nil
}

func installCron(options BootOptions, crontab string) (BootRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	current, err := options.Run(ctx, crontab, []string{"-l"}, nil)
	if err != nil {
		if !missingCrontab(current) {
			return BootRecord{}, fmt.Errorf("read crontab activation: %w: %s", err, strings.TrimSpace(string(current)))
		}
		current = nil
	}
	marker := "# cpak service restore"
	lines := make([]string, 0)
	for _, line := range strings.Split(string(current), "\n") {
		if line != "" && !strings.Contains(line, marker) {
			lines = append(lines, line)
		}
	}
	line := "@reboot " + shellQuote(options.Binary) + " service restore >/dev/null 2>&1 " + marker
	lines = append(lines, line)
	content := []byte(strings.Join(lines, "\n") + "\n")
	if output, err := options.Run(ctx, crontab, []string{"-"}, content); err != nil {
		return BootRecord{}, fmt.Errorf("install crontab activation: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return BootRecord{Adapter: "cron", Path: crontab, StartsBeforeLogin: true}, nil
}

func setSystemdUserEnabled(options BootOptions, enabled bool) error {
	action := "disable"
	if enabled {
		action = "enable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := options.Run(ctx, "systemctl", []string{"--user", action, "--now", "cpak.service"}, nil)
	if err != nil {
		return fmt.Errorf("%s cpak user service: %w: %s", action, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func missingCrontab(output []byte) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(string(output))), "no crontab for ")
}

func installXDGAutostart(options BootOptions) (BootRecord, error) {
	path := filepath.Join(options.ConfigHome, "autostart", "it.cpak.Service.desktop")
	entry := "[Desktop Entry]\nType=Application\nName=cpak service manager\nNoDisplay=true\n" +
		"Exec=" + desktopExecQuote(options.Binary) + " service restore\n"
	if err := writeFile(path, []byte(entry), 0644); err != nil {
		return BootRecord{}, err
	}
	return BootRecord{Adapter: "xdg-autostart", Path: path}, nil
}

func writeManualLauncher(options BootOptions) (string, error) {
	path := filepath.Join(options.ConfigHome, "cpak", "service-start")
	content := []byte("#!/bin/sh\nexec " + shellQuote(options.Binary) + " service restore\n")
	if err := writeFile(path, content, 0700); err != nil {
		return "", err
	}
	return path, nil
}

func saveBootRecord(directory string, record BootRecord) (BootRecord, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return BootRecord{}, err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return BootRecord{}, err
	}
	encoded = append(encoded, '\n')
	if err = writeFile(filepath.Join(directory, ".boot.json"), encoded, 0600); err != nil {
		return BootRecord{}, err
	}
	return record, nil
}

func LoadBootRecord(directory string) (BootRecord, error) {
	data, err := os.ReadFile(filepath.Join(directory, ".boot.json"))
	if err != nil {
		return BootRecord{}, err
	}
	var record BootRecord
	if err = json.Unmarshal(data, &record); err != nil {
		return BootRecord{}, err
	}
	return record, nil
}

func writeFile(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".cpak-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(mode); err == nil {
		_, err = temporary.Write(content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func runBootCommand(ctx context.Context, name string, arguments []string, input []byte) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	command.Stdin = bytes.NewReader(input)
	return command.CombinedOutput()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func systemdQuote(value string) string {
	return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "%", "%%").Replace(value) + "\""
}

func desktopExecQuote(value string) string {
	return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "%", "%%").Replace(value) + "\""
}
