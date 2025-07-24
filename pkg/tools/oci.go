/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package tools

import (
	"fmt"
	"strings"
)

// ValidateImageName checks if the given image name is in the correct format.
//
// Note: this method is not complete, it is just a basic check.
func ValidateImageName(image string) error {
	// TODO: this method is not complete, it only checks the image name
	if !strings.Contains(image, "/") {
		return fmt.Errorf("invalid image name: %s", image)
	}

	return nil
}
