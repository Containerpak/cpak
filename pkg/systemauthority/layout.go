/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	standardPrefix    = "/usr/local"
	busPolicyPath     = "/etc/dbus-1/system.d/it.cpak.SystemAuthority1.conf"
	polkitActionsPath = "/etc/polkit-1/actions"
	serviceFileName   = "it.cpak.SystemAuthority1.service"
	polkitPolicyName  = "it.cpak.system.policy"
)

// Image based systems keep the standard prefix read-only, so the integration
// falls back to the first prefix that accepts a privileged write.
var installPrefixes = []string{standardPrefix, "/opt/cpak", "/var/lib/cpak"}

type layout struct {
	prefix   string
	binary   string
	service  string
	polkit   string
	sessions string
}

func layoutFor(prefix string) layout {
	l := layout{
		prefix:   prefix,
		binary:   filepath.Join(prefix, "bin", "cpak"),
		service:  filepath.Join(prefix, "share", "dbus-1", "system-services", serviceFileName),
		polkit:   filepath.Join(prefix, "share", "polkit-1", "actions", polkitPolicyName),
		sessions: filepath.Join(prefix, "share", "wayland-sessions"),
	}
	// polkitd reads actions from /etc, /run and the two standard share
	// directories only, so a relocated prefix has to use the /etc one.
	if !l.standard() {
		l.polkit = filepath.Join(polkitActionsPath, polkitPolicyName)
	}
	return l
}

func (l layout) standard() bool {
	return l.prefix == standardPrefix
}

// dataDirectory is what belongs in XDG_DATA_DIRS: a display manager appends
// wayland-sessions to every entry itself.
func (l layout) dataDirectory() string {
	return filepath.Join(l.prefix, "share")
}

func (l layout) registry() Registry {
	return Registry{
		RegistryDirectory: DefaultRegistryDirectory,
		SessionDirectory:  l.sessions,
		SystemSessions:    DefaultSystemSessions,
		LauncherPath:      l.binary,
		OwnerUID:          0,
	}
}

// sessionSearchPath keeps the distribution sessions reachable: a display
// manager replaces its search path with the configured one, so dropping the
// directories it scans by default would hide every session already installed.
func (l layout) sessionSearchPath(standard []string) string {
	directories := []string{l.sessions}
	for _, path := range standard {
		if path != l.sessions {
			directories = append(directories, path)
		}
	}
	return strings.Join(directories, ":")
}

// serviceDirectory is declared to the bus only when it sits outside the
// standard search path; dbus honours a servicedir written in any file of an
// included configuration directory.
func (l layout) serviceDirectory() string {
	if l.standard() {
		return ""
	}
	return filepath.Dir(l.service)
}

func writableLayout() (layout, error) {
	for _, prefix := range installPrefixes {
		l := layoutFor(prefix)
		if err := probeWritable(filepath.Dir(l.binary)); err == nil {
			return l, nil
		}
	}
	return layout{}, errors.New("no writable prefix for the system integration")
}

func installedLayout() (layout, bool) {
	for _, prefix := range installPrefixes {
		l := layoutFor(prefix)
		if trustedFile(l.binary) {
			return l, true
		}
	}
	return layoutFor(standardPrefix), false
}

func probeWritable(directory string) error {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".cpak-")
	if err != nil {
		return err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return os.Remove(path)
}

func trustedFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
