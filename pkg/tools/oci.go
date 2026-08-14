/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
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
