/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A privileged step is escalated with whatever the host actually provides.
// pkexec and run0 ask through Polkit, which is what a graphical session has;
// sudo and doas prompt on the terminal, which is what a server has. None of
// them is assumed to exist.
var (
	graphicalEscalation = []string{"pkexec", "run0"}
	terminalEscalation  = []string{"sudo", "doas"}
)

func escalationTools() []string {
	if os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != "" {
		return append(append([]string{}, graphicalEscalation...), terminalEscalation...)
	}
	return append(append([]string{}, terminalEscalation...), graphicalEscalation...)
}

func escalationTool() (string, error) {
	candidates := escalationTools()
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no way to ask for administrator rights: install one of %s, or run this command as root",
		strings.Join(candidates, ", "))
}

// refuseSudoedStore stops the whole command from running as root: the store
// belongs to the user, and root would look for the package inside its own.
func refuseSudoedStore() error {
	if os.Geteuid() == 0 && os.Getenv("SUDO_UID") != "" {
		return errors.New("run this command as your own user: cpak asks for administrator rights only for the step that needs them")
	}
	return nil
}

// runPrivileged re-enters cpak for the one step that needs root. The command
// the user typed keeps running as the user, so it still reads the store that
// belongs to them.
func runPrivileged(arguments ...string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve cpak executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve cpak executable: %w", err)
	}
	tool, err := escalationTool()
	if err != nil {
		return err
	}
	command := exec.Command(tool, append([]string{executable}, arguments...)...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
