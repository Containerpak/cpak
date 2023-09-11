package tools

import (
	"fmt"
	"strings"
)

func ValidateImageName(image string) error {
	// TODO: this method is not complete, it only checks the image name
	if !strings.Contains(image, "/") {
		return fmt.Errorf("invalid image name: %s", image)
	}

	return nil
}
