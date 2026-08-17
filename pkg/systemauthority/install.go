/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

//go:embed assets/it.cpak.SystemAuthority1.service
var serviceFile []byte

//go:embed assets/it.cpak.SystemAuthority1.conf
var busPolicy []byte

//go:embed assets/it.cpak.system.policy
var polkitPolicy []byte

//go:embed assets/90-cpak-sessions.conf
var sddmConfig []byte

//go:embed assets/90-cpak-lightdm-sessions.conf
var lightdmConfig []byte

// Install returns the display managers that still need a manual step, so the
// caller can report them instead of leaving a session nobody can reach.
func Install() ([]string, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("system integration installation requires root")
	}
	target, err := writableLayout()
	if err != nil {
		return nil, err
	}
	if err := installExecutable(target.binary); err != nil {
		return nil, err
	}
	for _, file := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{target.service, renderAsset(serviceFile, "@BINARY@", target.binary), 0644},
		{busPolicyPath, renderAsset(busPolicy, "@SERVICEDIR@", servicedirElement(target)), 0644},
		{target.polkit, polkitPolicy, 0644},
	} {
		if err := ensureDirectory(filepath.Dir(file.path), 0); err != nil {
			return nil, fmt.Errorf("create system integration directory: %w", err)
		}
		if err := writeAtomic(file.path, file.data, file.mode); err != nil {
			return nil, fmt.Errorf("write system integration file: %w", err)
		}
	}
	if err := target.registry().Prepare(); err != nil {
		return nil, fmt.Errorf("prepare login session directory: %w", err)
	}
	return publishSessions(target)
}

func Uninstall() error {
	if os.Geteuid() != 0 {
		return errors.New("system integration removal requires root")
	}
	installed, _ := installedLayout()
	if err := installed.registry().Purge(); err != nil {
		return fmt.Errorf("remove registered login sessions: %w", err)
	}
	if err := unpublishSessions(); err != nil {
		return err
	}
	paths := []string{busPolicyPath}
	for _, prefix := range installPrefixes {
		candidate := layoutFor(prefix)
		paths = append(paths, candidate.service, candidate.polkit, candidate.binary)
	}
	for _, path := range paths {
		// A read-only prefix answers EROFS even for a missing file, so the
		// candidates that were never used must be skipped before unlinking.
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove system integration file: %w", err)
		}
	}
	return nil
}

func displayManagerExists(paths ...string) bool {
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return true
		}
	}
	return false
}

func Installed() bool {
	target, ok := installedLayout()
	if !ok {
		return false
	}
	for _, path := range []string{target.binary, target.service, busPolicyPath, target.polkit} {
		if !trustedFile(path) {
			return false
		}
	}
	return true
}

func renderAsset(asset []byte, pairs ...string) []byte {
	return []byte(strings.NewReplacer(pairs...).Replace(string(asset)))
}

func servicedirElement(target layout) string {
	directory := target.serviceDirectory()
	if directory == "" {
		return ""
	}
	return "  <servicedir>" + directory + "</servicedir>\n"
}

func installExecutable(destination string) error {
	input, err := os.Open("/proc/self/exe")
	if err != nil {
		return fmt.Errorf("open running cpak executable: %w", err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return errors.New("running cpak executable is not trusted")
	}
	if err := ensureDirectory(filepath.Dir(destination), 0); err != nil {
		return fmt.Errorf("create system binary directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".cpak-")
	if err != nil {
		return fmt.Errorf("create system cpak executable: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0755); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, input); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("copy cpak executable: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("install cpak executable: %w", err)
	}
	return nil
}
