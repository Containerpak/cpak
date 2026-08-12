/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systemauthority

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const (
	systemBinaryPath = "/usr/local/bin/cpak"
	serviceFilePath  = "/usr/local/share/dbus-1/system-services/it.cpak.SystemAuthority1.service"
	busPolicyPath    = "/etc/dbus-1/system.d/it.cpak.SystemAuthority1.conf"
	polkitPolicyPath = "/usr/local/share/polkit-1/actions/it.cpak.system.policy"
	sddmConfigPath   = "/etc/sddm.conf.d/90-cpak-sessions.conf"
)

//go:embed assets/it.cpak.SystemAuthority1.service
var serviceFile []byte

//go:embed assets/it.cpak.SystemAuthority1.conf
var busPolicy []byte

//go:embed assets/it.cpak.system.policy
var polkitPolicy []byte

//go:embed assets/90-cpak-sessions.conf
var sddmConfig []byte

func Install() error {
	if os.Geteuid() != 0 {
		return errors.New("system integration installation requires root")
	}
	if err := installExecutable(systemBinaryPath); err != nil {
		return err
	}
	for _, file := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{serviceFilePath, serviceFile, 0644},
		{busPolicyPath, busPolicy, 0644},
		{polkitPolicyPath, polkitPolicy, 0644},
	} {
		if err := ensureDirectory(filepath.Dir(file.path), 0); err != nil {
			return fmt.Errorf("create system integration directory: %w", err)
		}
		if err := writeAtomic(file.path, file.data, file.mode); err != nil {
			return fmt.Errorf("write system integration file: %w", err)
		}
	}
	if err := DefaultRegistry().Prepare(); err != nil {
		return fmt.Errorf("prepare login session directory: %w", err)
	}
	if displayManagerExists("/usr/bin/sddm", "/usr/local/bin/sddm") {
		if err := ensureDirectory(filepath.Dir(sddmConfigPath), 0); err != nil {
			return fmt.Errorf("create SDDM configuration directory: %w", err)
		}
		if err := writeAtomic(sddmConfigPath, sddmConfig, 0644); err != nil {
			return fmt.Errorf("write SDDM session configuration: %w", err)
		}
	}
	return nil
}

func Uninstall() error {
	if os.Geteuid() != 0 {
		return errors.New("system integration removal requires root")
	}
	if err := DefaultRegistry().Purge(); err != nil {
		return fmt.Errorf("remove registered login sessions: %w", err)
	}
	for _, path := range []string{serviceFilePath, busPolicyPath, polkitPolicyPath, sddmConfigPath, systemBinaryPath} {
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
	for _, path := range []string{systemBinaryPath, serviceFilePath, busPolicyPath, polkitPolicyPath} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return false
		}
	}
	return true
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
