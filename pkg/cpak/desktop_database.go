/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

func refreshDesktopDatabase() error {
	executable, err := exec.LookPath("update-desktop-database")
	if errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	directory := filepath.Join(home, ".local", "share", "applications")
	if _, err = os.Stat(directory); os.IsNotExist(err) {
		return nil
	}
	return exec.Command(executable, directory).Run()
}
