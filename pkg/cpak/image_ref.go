/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/mirkobrombin/cpak/pkg/oci"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func resolveManifestImage(manifest *types.CpakManifest, branch, release, commit string) (string, error) {
	if manifest.ImageRef == "" {
		return manifest.Image, nil
	}
	if manifest.ImageRef != "source" {
		return "", fmt.Errorf("unsupported image_ref: %s", manifest.ImageRef)
	}

	value := branch
	if release != "" {
		value = release
	}
	if commit != "" {
		value = commit
		if len(value) > 7 {
			value = value[:7]
		}
		value = "sha-" + value
	}
	if value == "" {
		value = "main"
	}

	tag := sourceImageTag(value)
	if tag == "" {
		return "", fmt.Errorf("Git source %q does not produce a valid OCI tag", value)
	}
	ref, err := oci.ParseReference(manifest.Image)
	if err != nil {
		return "", fmt.Errorf("parse image: %w", err)
	}
	if ref.IsDigest {
		return "", fmt.Errorf("image_ref source cannot be used with an image digest")
	}
	ref, err = ref.WithTag(tag)
	if err != nil {
		return "", err
	}
	return ref.Name(), nil
}

func sourceImageTag(value string) string {
	var tag strings.Builder
	lastDash := false
	for _, char := range value {
		valid := unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' || char == '.' || char == '-'
		if valid && char <= unicode.MaxASCII {
			tag.WriteRune(char)
			lastDash = false
			continue
		}
		if tag.Len() > 0 && !lastDash {
			tag.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(tag.String(), ".-")
	if len(result) > 128 {
		result = strings.TrimRight(result[:128], ".-")
	}
	return result
}
