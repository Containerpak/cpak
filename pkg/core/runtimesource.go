/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// MaxRuntimeSourceSize is the largest artifact a manifest may declare.
const MaxRuntimeSourceSize int64 = 2 << 30

var (
	// ErrRuntimeSourceInsecure is returned for an artifact that is not served
	// over https.
	ErrRuntimeSourceInsecure = errors.New("runtime source must be served over https")

	// ErrRuntimeSourceChecksum is returned when a downloaded artifact is not
	// the one the manifest declared.
	ErrRuntimeSourceChecksum = errors.New("runtime source checksum mismatch")

	// ErrRuntimeSourceSize is returned when a downloaded artifact is not the
	// size the manifest declared.
	ErrRuntimeSourceSize = errors.New("runtime source size mismatch")
)

// RuntimeSourceFileName answers the name an artifact is written under, which is
// the declared one when there is one and the last element of the URL otherwise.
func RuntimeSourceFileName(source types.RuntimeSource) string {
	if source.Name != "" {
		return source.Name
	}
	parsed, err := url.Parse(source.URL)
	if err != nil {
		return ""
	}
	return path.Base(parsed.Path)
}

// ValidateRuntimeSource decides whether an artifact may be fetched at all. It
// runs before anything is downloaded, because a transport, a checksum and a
// file name are the three things that cannot be checked afterwards without
// having already trusted them.
func ValidateRuntimeSource(source types.RuntimeSource) error {
	parsed, err := url.Parse(source.URL)
	if err != nil {
		return fmt.Errorf("invalid runtime source url %q: %w", source.URL, err)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return fmt.Errorf("%w: %s", ErrRuntimeSourceInsecure, source.URL)
	}
	if parsed.Host == "" {
		return fmt.Errorf("invalid runtime source url %q: no host", source.URL)
	}
	if !isSHA256(source.SHA256) {
		return fmt.Errorf("runtime source %s must declare a sha256 checksum", source.URL)
	}
	if source.Size <= 0 || source.Size > MaxRuntimeSourceSize {
		return fmt.Errorf("runtime source %s declares an invalid size of %d bytes", source.URL, source.Size)
	}
	if source.Installer != "dpkg" && source.Installer != "deb-extract" && source.Installer != "rpm" && source.Installer != "tar" && source.Installer != "file" {
		return fmt.Errorf("runtime source %s declares unsupported installer %q", source.URL, source.Installer)
	}
	if source.Installer == "file" {
		if filepath.Clean(source.Destination) != source.Destination || !strings.HasPrefix(source.Destination, "/opt/") {
			return fmt.Errorf("runtime source %s declares invalid file destination %q", source.URL, source.Destination)
		}
	} else if source.Destination != "" {
		return fmt.Errorf("runtime source %s declares a destination for installer %q", source.URL, source.Installer)
	}
	if source.Architecture != "" && source.Architecture != "amd64" && source.Architecture != "arm64" {
		return fmt.Errorf("runtime source %s declares unsupported architecture %q", source.URL, source.Architecture)
	}
	name := RuntimeSourceFileName(source)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid runtime source file name %q", name)
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
