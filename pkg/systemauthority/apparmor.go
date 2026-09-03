/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const appArmorProfileName = "cpak-userns"

var (
	appArmorRestrictionPath  = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"
	appArmorCurrentPath      = "/proc/self/attr/current"
	appArmorProfilePath      = "/etc/apparmor.d/cpak-userns"
	findAppArmorParser       = appArmorParser
	prepareAppArmorDirectory = func(path string) error {
		return ensureDirectory(path, 0)
	}
	runAppArmorParser = func(path string, arguments ...string) ([]byte, error) {
		return exec.Command(path, arguments...).CombinedOutput()
	}
)

//go:embed assets/cpak-userns
var appArmorProfile []byte

// AppArmorUserNamespacesRestricted reports whether this host requires an
// application profile before an unprivileged process may use user namespaces.
func AppArmorUserNamespacesRestricted() bool {
	value, err := os.ReadFile(appArmorRestrictionPath)
	return err == nil && strings.TrimSpace(string(value)) == "1"
}

// AppArmorRuntimeActive reports whether this process already runs under the
// cpak profile. Nested cpak processes inherit it across namespace creation.
func AppArmorRuntimeActive() bool {
	value, err := os.ReadFile(appArmorCurrentPath)
	return err == nil && strings.TrimSpace(string(value)) == appArmorProfileName+" (unconfined)"
}

// AppArmorRuntimeExecutable returns the root-owned cpak copy covered by the
// installed profile. A user-owned executable is never named by that profile.
func AppArmorRuntimeExecutable() (string, bool) {
	if !AppArmorUserNamespacesRestricted() {
		return "", false
	}
	target, found := installedLayout()
	if !found || !appArmorProfileTrusted(target) || !trustedFile(target.storage) {
		return "", false
	}
	return target.binary, true
}

// SameExecutableContents checks that the user copy and the profiled system
// copy are the same cpak release before a runtime command changes executable.
func SameExecutableContents(first, second string) bool {
	if sameFile(first, second) {
		return true
	}
	firstDigest, firstErr := executableDigest(first)
	secondDigest, secondErr := executableDigest(second)
	return firstErr == nil && secondErr == nil && firstDigest == secondDigest
}

func executableDigest(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result, nil
}

func installAppArmorProfile(target layout) error {
	if !AppArmorUserNamespacesRestricted() {
		return nil
	}
	parser, err := findAppArmorParser()
	if err != nil {
		return err
	}
	if err = prepareAppArmorDirectory(filepath.Dir(appArmorProfilePath)); err != nil {
		return fmt.Errorf("create AppArmor profile directory: %w", err)
	}
	profile := renderedAppArmorProfile(target.binary)
	candidate, err := writeAppArmorCandidate(filepath.Dir(appArmorProfilePath), profile)
	if err != nil {
		return fmt.Errorf("write AppArmor profile: %w", err)
	}
	defer os.Remove(candidate)
	if output, loadErr := runAppArmorParser(parser, "-r", candidate); loadErr != nil {
		return fmt.Errorf("load AppArmor profile: %w: %s", loadErr, strings.TrimSpace(string(output)))
	}
	if err = os.Rename(candidate, appArmorProfilePath); err != nil {
		return fmt.Errorf("publish AppArmor profile: %w", err)
	}
	return nil
}

func writeAppArmorCandidate(directory string, profile []byte) (string, error) {
	file, err := os.CreateTemp(directory, ".cpak-userns-")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err = file.Chmod(0644); err == nil {
		_, err = file.Write(profile)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func removeAppArmorProfile() error {
	if _, err := os.Lstat(appArmorProfilePath); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect AppArmor profile: %w", err)
	}
	parser, err := findAppArmorParser()
	if err != nil {
		return err
	}
	if output, unloadErr := runAppArmorParser(parser, "-R", appArmorProfilePath); unloadErr != nil {
		return fmt.Errorf("unload AppArmor profile: %w: %s", unloadErr, strings.TrimSpace(string(output)))
	}
	if err = os.Remove(appArmorProfilePath); err != nil {
		return fmt.Errorf("remove AppArmor profile: %w", err)
	}
	return nil
}

func renderedAppArmorProfile(binary string) []byte {
	return renderAsset(appArmorProfile, "@BINARY@", binary)
}

func appArmorProfileTrusted(target layout) bool {
	if !trustedFile(appArmorProfilePath) {
		return false
	}
	profile, err := os.ReadFile(appArmorProfilePath)
	return err == nil && string(profile) == string(renderedAppArmorProfile(target.binary))
}

func appArmorParser() (string, error) {
	for _, path := range []string{"/usr/sbin/apparmor_parser", "/sbin/apparmor_parser"} {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	if path, err := exec.LookPath("apparmor_parser"); err == nil {
		return path, nil
	}
	return "", errors.New("AppArmor restricts user namespaces but apparmor_parser is unavailable")
}
