/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package tools

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/oci"
)

// ValidateImageName checks if the given image name is in the correct format.
//
// Note: this method is not complete, it is just a basic check.
func ValidateImageName(image string) error {
	if _, err := oci.ParseReference(image); err != nil {
		return fmt.Errorf("invalid image name: %w", err)
	}

	return nil
}
