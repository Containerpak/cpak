/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Backend identifies a supported desktop dialog implementation.
type Backend string

const (
	BackendAuto    Backend = "auto"
	BackendBuiltin Backend = "builtin"
	BackendAdwaita Backend = "adwaita"
	BackendGTK     Backend = "gtk"
	BackendKDE     Backend = "kde"
	BackendQt      Backend = "qt"
	BackendGNOME   Backend = "gnome"
)

// Config controls desktop integration selected by the user or distribution.
type Config struct {
	Desktop DesktopConfig `json:"desktop"`
}

// DesktopConfig selects the preferred dialog backend.
type DesktopConfig struct {
	DialogBackend Backend `json:"dialog_backend"`
}

var defaultBackend = "auto"

// LoadConfig reads the first available desktop configuration file.
func LoadConfig() Config {
	paths := configPaths()
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var config Config
		if json.Unmarshal(content, &config) == nil {
			return config
		}
	}
	return Config{}
}

// SelectBackend resolves the configured backend and required desktop tools.
func SelectBackend(preferred Backend) Backend {
	if preferred == "" {
		preferred = Backend(os.Getenv("CPAK_UI_ADAPTER"))
	}
	if preferred == "" {
		preferred = LoadConfig().Desktop.DialogBackend
	}
	if preferred == "" {
		preferred = Backend(defaultBackend)
	}
	switch preferred {
	case BackendBuiltin:
		return BackendBuiltin
	case BackendGNOME:
		return firstAvailable(BackendAdwaita, BackendBuiltin)
	case BackendAdwaita, BackendGTK, BackendKDE, BackendQt:
		return firstAvailable(preferred, BackendBuiltin)
	case BackendAuto:
		desktop := strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP"))
		if strings.Contains(desktop, "kde") || strings.Contains(desktop, "plasma") {
			return firstAvailable(BackendKDE, BackendQt, BackendBuiltin)
		}
		if strings.Contains(desktop, "gnome") {
			return firstAvailable(BackendAdwaita, BackendBuiltin)
		}
		if strings.Contains(desktop, "mate") || strings.Contains(desktop, "xfce") || strings.Contains(desktop, "cinnamon") || strings.Contains(desktop, "unity") {
			return firstAvailable(BackendGTK, BackendBuiltin)
		}
		if strings.Contains(desktop, "lxqt") {
			return firstAvailable(BackendQt, BackendBuiltin)
		}
		return BackendBuiltin
	default:
		return BackendBuiltin
	}
}

// Install shows a native confirmation and progress dialog.
func Install(backend Backend, name, description, origin string, action func(func(string)) error) (bool, error) {
	started := false
	wrapped := func(progress func(string)) error {
		started = true
		return action(progress)
	}
	var err error
	switch backend {
	case BackendAdwaita, BackendGTK, BackendKDE, BackendQt:
		result, promptErr := runAdapterPrompt(context.Background(), backend, adapterPrompt{
			Title: "Install " + name, Heading: name, Body: description + "\n\n" + origin,
			AcceptLabel: "Install", CancelLabel: "Cancel", Recommended: true,
		})
		if promptErr != nil {
			return false, nil
		}
		if !result.Accepted {
			return true, nil
		}
		err = Progress(backend, ProgressRequest{Title: "Installing " + name, Heading: "Installing " + name, Detail: "Preparing cpak"}, func(progress func(ProgressUpdate)) error {
			return wrapped(func(message string) {
				progress(ProgressUpdate{Message: message})
			})
		})
	default:
		return false, nil
	}
	if err != nil && !started {
		return false, nil
	}
	return true, err
}

func firstAvailable(backends ...Backend) Backend {
	for _, backend := range backends {
		if backend == BackendBuiltin {
			return BackendBuiltin
		}
		if _, err := adapterExecutable(backend); err == nil {
			return backend
		}
	}
	return BackendBuiltin
}

func configPaths() []string {
	if path := os.Getenv("CPAK_OPTS_FILE"); path != "" {
		return []string{path}
	}
	paths := []string{}
	if directory, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(directory, "cpak", "cpak.json"))
	}
	return append(paths, "/etc/cpak/cpak.json", "/usr/share/cpak/cpak.json")
}

func escapeMarkup(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}
