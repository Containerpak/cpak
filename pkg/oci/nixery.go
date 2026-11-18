/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package oci

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// NixeryReference describes a build request, never an install-time image pin.
func NixeryReference(registry string, packages []string, architecture string) (Reference, error) {
	if registry == "" {
		registry = "nixery.dev"
	}
	if len(packages) == 0 {
		return Reference{}, errors.New("oci: at least one Nixery package is required")
	}
	parts := make([]string, 0, len(packages)+1)
	switch architecture {
	case "amd64":
	case "arm64":
		parts = append(parts, "arm64")
	default:
		return Reference{}, fmt.Errorf("oci: unsupported Nixery architecture %q", architecture)
	}
	for _, pkg := range packages {
		if pkg == "" || pkg == "arm64" || strings.ContainsAny(pkg, "/:@") {
			return Reference{}, fmt.Errorf("oci: invalid Nixery package %q", pkg)
		}
		parts = append(parts, pkg)
	}
	ref, err := ParseReference(registry + "/" + strings.Join(parts, "/"))
	if err != nil {
		return Reference{}, err
	}
	if ref.Registry != registry || ref.Repository != strings.Join(parts, "/") || ref.Identifier != "latest" {
		return Reference{}, errors.New("oci: Nixery registry must be a hostname with an optional port")
	}
	return ref, nil
}

// Pin verifies that the resolved image is also retrievable by its immutable digest.
func (c *Client) Pin(ctx context.Context, value string, platform Platform) (string, error) {
	image, err := c.ResolvePlatform(ctx, value, platform)
	if err != nil {
		return "", err
	}
	pinned := image.Reference.ContextName() + "@" + image.Digest
	if !image.Reference.IsDigest {
		if _, err = c.ResolvePlatform(ctx, pinned, platform); err != nil {
			return "", fmt.Errorf("oci: retrieve resolved image by digest: %w", err)
		}
	}
	return pinned, nil
}
