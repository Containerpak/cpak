/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A display manager reaches the session directory in one of two ways. SDDM and
// LightDM read it from a configuration key, which behaves the same under every
// init. GDM and the greetd greeters only read XDG_DATA_DIRS from the service
// environment, so a relocated directory has to be published through whichever
// init supervises them.
const (
	sddmConfigPath    = "/etc/sddm.conf.d/90-cpak-sessions.conf"
	lightdmConfigPath = "/etc/lightdm/lightdm.conf.d/90-cpak-sessions.conf"
	standardDataPath  = "/usr/local/share:/usr/share"
	dataDirsVariable  = "XDG_DATA_DIRS"
	environmentMarker = "# cpak session directory"
)

var (
	sddmSessions    = []string{filepath.Join(standardPrefix, "share", "wayland-sessions"), DefaultSystemSessions}
	lightdmSessions = []string{"/usr/share/lightdm/sessions", "/usr/share/xsessions", DefaultSystemSessions}
)

type environmentManager struct {
	name     string
	binaries []string
	services []string
}

var environmentManagers = []environmentManager{
	{
		name:     "GDM",
		binaries: []string{"/usr/sbin/gdm", "/usr/sbin/gdm3", "/usr/bin/gdm", "/usr/bin/gdm3"},
		services: []string{"gdm", "gdm3"},
	},
	{
		name:     "greetd",
		binaries: []string{"/usr/sbin/greetd", "/usr/bin/greetd", "/usr/local/bin/greetd"},
		services: []string{"greetd"},
	},
}

// publishSessions points every installed display manager at the session
// directory and reports the ones that need a manual step.
func publishSessions(target layout) ([]string, error) {
	if displayManagerExists("/usr/bin/sddm", "/usr/local/bin/sddm") {
		config := renderAsset(sddmConfig, "@SESSIONS@", target.sessionSearchPath(sddmSessions))
		if err := writeSystemFile(sddmConfigPath, config); err != nil {
			return nil, fmt.Errorf("write SDDM session configuration: %w", err)
		}
	}
	if displayManagerExists("/usr/sbin/lightdm", "/usr/bin/lightdm", "/usr/local/sbin/lightdm", "/usr/local/bin/lightdm") {
		config := renderAsset(lightdmConfig, "@SESSIONS@", target.sessionSearchPath(lightdmSessions))
		if err := writeSystemFile(lightdmConfigPath, config); err != nil {
			return nil, fmt.Errorf("write LightDM session configuration: %w", err)
		}
	}
	// A standard prefix already sits in the search path every display manager
	// inherits, so nothing has to be published to the init.
	if target.standard() {
		return nil, nil
	}
	pending := []string{}
	supervisor := detectInit()
	for _, manager := range environmentManagers {
		if !displayManagerExists(manager.binaries...) {
			continue
		}
		note, err := publishDataDirectory(supervisor, manager, target.dataDirectory())
		if err != nil {
			return nil, err
		}
		if note != "" {
			pending = append(pending, note)
		}
	}
	return pending, nil
}

func unpublishSessions() error {
	paths := []string{sddmConfigPath, lightdmConfigPath}
	for _, manager := range environmentManagers {
		for _, service := range manager.services {
			paths = append(paths, systemdOverridePath(service))
		}
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove session configuration: %w", err)
		}
	}
	for _, manager := range environmentManagers {
		for _, service := range manager.services {
			if err := removeManagedBlock(openRCConfigPath(service)); err != nil {
				return err
			}
		}
	}
	return nil
}

func publishDataDirectory(supervisor string, manager environmentManager, directory string) (string, error) {
	switch supervisor {
	case "systemd":
		return "", writeSystemdOverride(manager, directory)
	case "openrc":
		return "", writeShellOverride(manager, directory, openRCConfigPath)
	case "runit", "s6", "s6-svscan", "dinit", "sysvinit":
		// These inits carry no per service environment file of their own: the
		// run scripts shipped by the distributions exec the daemon directly,
		// so writing one would produce a file nothing reads.
		return manualStep(manager, directory, supervisor+" has no service environment file the packaged scripts read"), nil
	default:
		return manualStep(manager, directory, "this init exposes no supported way to set a service environment"), nil
	}
}

func manualStep(manager environmentManager, directory, reason string) string {
	return fmt.Sprintf("%s was not configured automatically: %s. Add %s to %s in its service environment.",
		manager.name, reason, directory, dataDirsVariable)
}

func writeSystemdOverride(manager environmentManager, directory string) error {
	for _, service := range manager.services {
		if !systemdServiceExists(service) {
			continue
		}
		path := systemdOverridePath(service)
		value := mergeDataDirectories(directory, systemdDataDirectories(service))
		content := "[Service]\n" + environmentMarker + "\nEnvironment=\"" + dataDirsVariable + "=" + value + "\"\n"
		if err := writeSystemFile(path, []byte(content)); err != nil {
			return fmt.Errorf("write %s session environment: %w", manager.name, err)
		}
	}
	return nil
}

func writeShellOverride(manager environmentManager, directory string, resolve func(string) string) error {
	for _, service := range manager.services {
		path := resolve(service)
		if _, err := os.Stat(filepath.Dir(path)); err != nil {
			continue
		}
		existing, current := readManagedBlock(path)
		value := mergeDataDirectories(directory, current)
		block := environmentMarker + "\nexport " + dataDirsVariable + "=\"" + value + "\"\n"
		if err := writeSystemFile(path, []byte(existing+block)); err != nil {
			return fmt.Errorf("write %s session environment: %w", manager.name, err)
		}
	}
	return nil
}

// mergeDataDirectories keeps the directories the service already searches and
// puts the session directory first, so no session disappears from the greeter.
func mergeDataDirectories(directory, current string) string {
	if current == "" {
		current = standardDataPath
	}
	merged := []string{directory}
	for _, entry := range strings.Split(current, ":") {
		if entry != "" && entry != directory {
			merged = append(merged, entry)
		}
	}
	return strings.Join(merged, ":")
}

func systemdServiceExists(service string) bool {
	for _, root := range []string{"/etc/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"} {
		if _, err := os.Stat(filepath.Join(root, service+".service")); err == nil {
			return true
		}
	}
	return false
}

// systemdOverridePath sorts after any drop-in already present, because systemd
// lets the last assignment of a variable win.
func systemdOverridePath(service string) string {
	return filepath.Join("/etc/systemd/system", service+".service.d", "zz-cpak-sessions.conf")
}

func openRCConfigPath(service string) string {
	return filepath.Join("/etc/conf.d", service)
}

// systemdDataDirectories reads the value the service is already given, so a
// drop-in written by another project is preserved instead of replaced.
func systemdDataDirectories(service string) string {
	value := ""
	for _, root := range []string{"/usr/lib/systemd/system", "/lib/systemd/system", "/etc/systemd/system"} {
		directory := filepath.Join(root, service+".service.d")
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".conf") {
				names = append(names, entry.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			if name == filepath.Base(systemdOverridePath(service)) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil {
				continue
			}
			if found := dataDirectoriesFromUnit(string(data)); found != "" {
				value = found
			}
		}
	}
	return value
}

func dataDirectoriesFromUnit(content string) string {
	value := ""
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Environment=") {
			continue
		}
		assignment := strings.Trim(strings.TrimPrefix(line, "Environment="), "\"")
		name, directories, found := strings.Cut(assignment, "=")
		if found && name == dataDirsVariable {
			value = directories
		}
	}
	return value
}

func readManagedBlock(path string) (string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	content := string(data)
	index := strings.Index(content, environmentMarker)
	if index < 0 {
		return content, shellDataDirectories(content)
	}
	return content[:index], shellDataDirectories(content[:index])
}

func removeManagedBlock(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	content := string(data)
	index := strings.Index(content, environmentMarker)
	if index < 0 {
		return nil
	}
	kept := content[:index]
	if strings.TrimSpace(kept) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove session environment: %w", err)
		}
		return nil
	}
	if err := writeSystemFile(path, []byte(kept)); err != nil {
		return fmt.Errorf("rewrite session environment: %w", err)
	}
	return nil
}

func shellDataDirectories(content string) string {
	value := ""
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		name, directories, found := strings.Cut(line, "=")
		if found && name == dataDirsVariable {
			value = strings.Trim(directories, "\"'")
		}
	}
	return value
}

func detectInit() string {
	data, err := os.ReadFile("/proc/1/comm")
	if err != nil {
		return ""
	}
	switch name := strings.TrimSpace(string(data)); name {
	case "systemd":
		return "systemd"
	case "openrc-init":
		return "openrc"
	case "runit":
		return "runit"
	case "init":
		// sysvinit and OpenRC share this name, and only OpenRC keeps a runtime
		// directory of its own.
		if _, err := os.Stat("/run/openrc"); err == nil {
			return "openrc"
		}
		return "sysvinit"
	default:
		return name
	}
}

func writeSystemFile(path string, data []byte) error {
	if err := ensureDirectory(filepath.Dir(path), 0); err != nil {
		return fmt.Errorf("create configuration directory: %w", err)
	}
	return writeAtomic(path, data, 0644)
}
