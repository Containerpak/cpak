/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func resolveManifestOrigin(manifestPath, explicit string, manifest *types.CpakManifest) (string, error) {
	if explicit != "" {
		return normalizeRepositoryOrigin(explicit)
	}
	directory, err := filepath.Abs(filepath.Dir(manifestPath))
	if err == nil {
		output, gitErr := exec.Command("git", "-C", directory, "remote", "get-url", "origin").Output()
		if gitErr == nil {
			if origin, normalizeErr := normalizeRepositoryOrigin(strings.TrimSpace(string(output))); normalizeErr == nil {
				return origin, nil
			}
		}
	}
	return localPackageOrigin("package", manifest)
}

func normalizeRepositoryOrigin(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("repository origin is empty")
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" {
			return "", fmt.Errorf("invalid repository origin: %s", value)
		}
		value = parsed.Host + "/" + strings.TrimPrefix(parsed.Path, "/")
	} else if at := strings.Index(value, "@"); at >= 0 {
		hostPath := value[at+1:]
		separator := strings.Index(hostPath, ":")
		if separator < 1 {
			return "", fmt.Errorf("invalid repository origin: %s", value)
		}
		value = hostPath[:separator] + "/" + hostPath[separator+1:]
	}
	value = strings.TrimSuffix(value, ".git")
	value = strings.Trim(value, "/")
	parts := strings.Split(value, "/")
	if len(parts) != 3 {
		return "", fmt.Errorf("repository origin must contain host, owner, and repository: %s", value)
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\ \t\n\r") {
			return "", fmt.Errorf("invalid repository origin: %s", value)
		}
	}
	return cpak.NormalizeRepositoryOrigin(value)
}
