/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Backend identifies a supported desktop dialog implementation.
type Backend string

const (
	BackendAuto    Backend = "auto"
	BackendBuiltin Backend = "builtin"
	BackendGNOME   Backend = "gnome"
	BackendKDE     Backend = "kde"
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
		preferred = LoadConfig().Desktop.DialogBackend
	}
	if preferred == "" {
		preferred = Backend(defaultBackend)
	}
	switch preferred {
	case BackendBuiltin:
		return BackendBuiltin
	case BackendGNOME:
		if available(BackendGNOME) {
			return BackendGNOME
		}
		return BackendBuiltin
	case BackendKDE:
		if available(BackendKDE) {
			return BackendKDE
		}
		return BackendBuiltin
	case BackendAuto:
		desktop := strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP"))
		if strings.Contains(desktop, "kde") && available(BackendKDE) {
			return BackendKDE
		}
		if (strings.Contains(desktop, "gnome") || strings.Contains(desktop, "unity") || strings.Contains(desktop, "cinnamon")) && available(BackendGNOME) {
			return BackendGNOME
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
	case BackendGNOME:
		err = installGNOME(name, description, origin, wrapped)
	case BackendKDE:
		err = installKDE(name, description, origin, wrapped)
	default:
		return false, nil
	}
	if err != nil && !started {
		return false, nil
	}
	return true, err
}

func installGNOME(name, description, origin string, action func(func(string)) error) error {
	path, err := exec.LookPath("zenity")
	if err != nil {
		return err
	}
	message := fmt.Sprintf("<b>%s</b>\n\n%s\n\n%s", escapeMarkup(name), escapeMarkup(description), escapeMarkup(origin))
	confirm := exec.Command(path, "--question", "--title=Install "+name, "--text="+message, "--ok-label=Install", "--cancel-label=Cancel", "--width=480")
	if err = confirm.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil
		}
		return err
	}
	progress := exec.Command(path, "--progress", "--pulsate", "--auto-close", "--no-cancel", "--title=Installing "+name, "--text=Preparing cpak", "--width=480")
	input, err := progress.StdinPipe()
	if err != nil {
		return err
	}
	if err = progress.Start(); err != nil {
		return err
	}
	actionErr := action(func(message string) {
		_, _ = fmt.Fprintf(input, "# %s\n", strings.ReplaceAll(message, "\n", " "))
	})
	_ = input.Close()
	progressErr := progress.Wait()
	if actionErr != nil {
		showGNOMEError(path, name, actionErr)
		return actionErr
	}
	return progressErr
}

func installKDE(name, description, origin string, action func(func(string)) error) error {
	path, err := exec.LookPath("kdialog")
	if err != nil {
		return err
	}
	message := name + "\n\n" + description + "\n\n" + origin
	confirm := exec.Command(path, "--title", "Install "+name, "--yesno", message, "--yes-label", "Install", "--no-label", "Cancel")
	if err = confirm.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil
		}
		return err
	}
	progress := exec.Command(path, "--title", "Installing "+name, "--passivepopup", "Preparing cpak", "300")
	if err = progress.Start(); err != nil {
		return err
	}
	actionErr := action(func(string) {})
	_ = progress.Process.Kill()
	_, _ = progress.Process.Wait()
	if actionErr != nil {
		_ = exec.Command(path, "--title", name, "--error", actionErr.Error()).Run()
		return actionErr
	}
	return exec.Command(path, "--title", name, "--msgbox", name+" is installed").Run()
}

func showGNOMEError(path, name string, err error) {
	_ = exec.Command(path, "--error", "--title="+name, "--text="+err.Error(), "--width=480").Run()
}

func available(backend Backend) bool {
	name := ""
	switch backend {
	case BackendGNOME:
		name = "zenity"
	case BackendKDE:
		name = "kdialog"
	}
	_, err := exec.LookPath(name)
	return name != "" && err == nil
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
