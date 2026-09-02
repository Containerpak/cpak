/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/mirkobrombin/cpak/pkg/types"
)

const (
	runtimeEnvironmentFileLimit = 1024 * 1024
	runtimeSecretFileLimit      = 1024 * 1024
)

var runtimeNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (c *Cpak) ConfigureRuntime(environment, environmentFiles, secrets []string) error {
	resolved, err := resolveRuntimeEnvironment(environment, environmentFiles)
	if err != nil {
		return err
	}
	resolvedSecrets, err := resolveRuntimeSecrets(secrets)
	if err != nil {
		return err
	}
	c.runtimeEnvironment = resolved
	c.runtimeSecrets = resolvedSecrets
	return nil
}

func resolveRuntimeEnvironment(entries, files []string) ([]string, error) {
	values := make(map[string]string)
	for _, file := range files {
		parsed, err := readRuntimeEnvironmentFile(file)
		if err != nil {
			return nil, err
		}
		for _, entry := range parsed {
			name, value, _ := strings.Cut(entry, "=")
			values[name] = value
		}
	}
	for _, entry := range entries {
		name, value, err := parseRuntimeEnvironmentEntry(entry)
		if err != nil {
			return nil, err
		}
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	resolved := make([]string, 0, len(names))
	for _, name := range names {
		resolved = append(resolved, name+"="+values[name])
	}
	return resolved, nil
}

func readRuntimeEnvironmentFile(path string) ([]string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("environment file path must be absolute: %q", path)
	}
	file, err := openPrivateRuntimeFile(path, false)
	if err != nil {
		return nil, fmt.Errorf("open environment file: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(io.LimitReader(file, runtimeEnvironmentFileLimit+1))
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read environment file: %w", err)
	}
	if len(data) > runtimeEnvironmentFileLimit {
		return nil, fmt.Errorf("environment file exceeds %d bytes", runtimeEnvironmentFileLimit)
	}
	entries := make([]string, 0)
	for number, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, value, parseErr := parseRuntimeEnvironmentEntry(line)
		if parseErr != nil {
			return nil, fmt.Errorf("environment file %s line %d: %w", path, number+1, parseErr)
		}
		entries = append(entries, name+"="+value)
	}
	return entries, nil
}

func parseRuntimeEnvironmentEntry(entry string) (string, string, error) {
	name, value, found := strings.Cut(entry, "=")
	if !found || !runtimeNamePattern.MatchString(name) {
		return "", "", errors.New("environment entry must use NAME=value")
	}
	if strings.HasPrefix(name, "CPAK_") {
		return "", "", errors.New("environment entry cannot replace a CPAK_ variable")
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", "", errors.New("environment value contains a control character")
	}
	return name, value, nil
}

func resolveRuntimeSecrets(entries []string) ([]types.RuntimeSecret, error) {
	secrets := make([]types.RuntimeSecret, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name, source, found := strings.Cut(entry, "=")
		if !found || !runtimeNamePattern.MatchString(name) {
			return nil, errors.New("secret must use NAME=/absolute/path")
		}
		if seen[name] {
			return nil, fmt.Errorf("secret %s is configured more than once", name)
		}
		if !filepath.IsAbs(source) || filepath.Clean(source) != source {
			return nil, fmt.Errorf("secret %s source must be an absolute path", name)
		}
		file, err := openPrivateRuntimeFile(source, true)
		if err != nil {
			return nil, fmt.Errorf("secret %s: %w", name, err)
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, fmt.Errorf("secret %s: %w", name, closeErr)
		}
		seen[name] = true
		secrets = append(secrets, types.RuntimeSecret{Name: name, Source: source})
	}
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].Name < secrets[j].Name })
	return secrets, nil
}

func openPrivateRuntimeFile(path string, requirePrivate bool) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("source is not a regular file")
	}
	if requirePrivate && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("source must not be accessible by group or other users")
	}
	if requirePrivate {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Getuid()) {
			return nil, errors.New("source is not owned by the current user")
		}
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !os.SameFile(info, opened) {
		file.Close()
		return nil, errors.New("source changed while it was opened")
	}
	return file, nil
}

func applyRuntimeEnvironment(base, runtime []string) []string {
	for _, entry := range runtime {
		name, _, _ := strings.Cut(entry, "=")
		base = setEnvironmentValue(base, name, strings.TrimPrefix(entry, name+"="))
	}
	return base
}

func (c *Cpak) runtimeIdentity() (string, error) {
	if len(c.runtimeEnvironment) == 0 && len(c.runtimeSecrets) == 0 {
		return "", nil
	}
	type secretIdentity struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	}
	identity := struct {
		Environment []string         `json:"environment,omitempty"`
		Secrets     []secretIdentity `json:"secrets,omitempty"`
	}{Environment: c.runtimeEnvironment}
	for _, secret := range c.runtimeSecrets {
		file, err := openPrivateRuntimeFile(secret.Source, true)
		if err != nil {
			return "", fmt.Errorf("secret %s: %w", secret.Name, err)
		}
		hash := sha256.New()
		written, copyErr := io.Copy(hash, io.LimitReader(file, runtimeSecretFileLimit+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return "", fmt.Errorf("read secret %s: %w", secret.Name, errors.Join(copyErr, closeErr))
		}
		if written > runtimeSecretFileLimit {
			return "", fmt.Errorf("secret %s exceeds %d bytes", secret.Name, runtimeSecretFileLimit)
		}
		identity.Secrets = append(identity.Secrets, secretIdentity{Name: secret.Name, Digest: hex.EncodeToString(hash.Sum(nil))})
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}
