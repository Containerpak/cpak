/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// flatpakLaunchArguments are what a Flatpak application needs from spawn on
// top of what any application gets: its linker cache indexed at every launch,
// so the driver libraries cpak mounts are found, and the host fonts, read-only,
// where the compatibility layer told fontconfig to look.
func (c *Cpak) flatpakLaunchArguments(app types.Application) []string {
	if app.Flatpak == nil {
		return nil
	}
	arguments := []string{"--linker-cache"}
	shared := [][2]string{
		{"/usr/share/fonts", "/run/host/fonts"},
		{"/usr/local/share/fonts", "/run/host/local-fonts"},
	}
	home, homeErr := os.UserHomeDir()
	if homeErr == nil {
		shared = append(shared, [2]string{filepath.Join(home, ".local", "share", "fonts"), "/run/host/user-fonts"})
	}
	// The runtimes already look for icons under /run/host/share and
	// /run/host/user-share, which is where Flatpak puts the host's.
	if c.Options.FlatpakHostIcons {
		shared = append(shared, [2]string{"/usr/share/icons", "/run/host/share/icons"})
		if homeErr == nil {
			shared = append(shared, [2]string{filepath.Join(home, ".local", "share", "icons"), "/run/host/user-share/icons"})
		}
	}
	for _, entry := range shared {
		if info, err := os.Stat(entry[0]); err == nil && info.IsDir() {
			arguments = append(arguments, "--extra-links", entry[0]+":"+entry[1])
		}
	}
	return arguments
}

// flatpakIconTheme is the icon theme the settings portal reports.
func (c *Cpak) flatpakIconTheme() string {
	if c.Options.FlatpakIconTheme != "" {
		return c.Options.FlatpakIconTheme
	}
	if c.Options.FlatpakHostIcons {
		if theme := hostGTKIconTheme(); theme != "" {
			return theme
		}
	}
	return "Adwaita"
}

// hostGTKIconTheme reads the icon theme the host's GTK is configured with
// from its settings files, which needs no session bus.
func hostGTKIconTheme() string {
	var files []string
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		files = append(files, filepath.Join(configHome, "gtk-4.0", "settings.ini"), filepath.Join(configHome, "gtk-3.0", "settings.ini"))
	} else if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".config", "gtk-4.0", "settings.ini"), filepath.Join(home, ".config", "gtk-3.0", "settings.ini"))
	}
	files = append(files, "/etc/gtk-4.0/settings.ini", "/etc/gtk-3.0/settings.ini")
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			key, value, found := strings.Cut(line, "=")
			if found && strings.TrimSpace(key) == "gtk-icon-theme-name" {
				if theme := strings.Trim(strings.TrimSpace(value), `"`); validThemeName(theme) {
					return theme
				}
			}
		}
	}
	return ""
}

func validThemeName(name string) bool {
	if name == "" || len(name) > 128 || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\x00\n")
}

// flatpakPortalArguments start the desktop bus proxy as the sandbox's own
// session bus: the toolkits of a Flatpak runtime talk to portals, and every
// portal they reach is answered by cpak and gated by the application's
// policy, without a host session bus anywhere in the path.
func flatpakPortalArguments(container types.Container, app types.Application, override types.Override, iconTheme string) []string {
	arguments := []string{"--portals", "--app-name", app.Name, "--exec-socket", container.ExecSocketPath, "--icon-theme", iconTheme}
	if scheme := hostColorScheme(); scheme != 0 {
		arguments = append(arguments, "--color-scheme", strconv.Itoa(int(scheme)))
	}
	if override.SessionBus.Enabled() {
		if policy, err := json.Marshal(override.SessionBus); err == nil {
			arguments = append(arguments, "--policy", base64.RawURLEncoding.EncodeToString(policy))
		}
	}
	if override.Network {
		arguments = append(arguments, "--network-monitor")
	}
	brokered := false
	if override.FilePicker.Enabled() {
		arguments = append(arguments, "--file-picker")
		brokered = true
	}
	if override.Notification {
		arguments = append(arguments, "--notifications")
		brokered = true
	}
	if override.OpenURI {
		arguments = append(arguments, "--open-uri")
		brokered = true
	}
	if brokered && container.SystemBrokerSocketPath != "" {
		arguments = append(arguments,
			"--broker-socket-path", container.SystemBrokerSocketPath,
			"--token-file", container.SystemBrokerTokenPath,
		)
	}
	return arguments
}

// hostColorScheme reads the preference cpak already passes on as GTK_THEME,
// so the portal answers what the application's theme was chosen from.
func hostColorScheme() uint32 {
	theme := strings.ToLower(detectedHostGTKTheme())
	switch {
	case strings.HasSuffix(theme, ":dark") || strings.HasSuffix(theme, "-dark"):
		return 1
	case strings.HasSuffix(theme, ":light"):
		return 2
	}
	return 0
}
