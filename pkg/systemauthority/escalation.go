/*
 * Copyright (c) 2026 Fabricators
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runPrivilegedEnrolment carries the exact request into cpak's root role when
// neither transport can authorize it. Only this step changes identity; the
// install keeps running as the user whose store it belongs to.
func runPrivilegedEnrolment(message socketRequest) error {
	var input bytes.Buffer
	if err := json.NewEncoder(&input).Encode(message); err != nil {
		return fmt.Errorf("encode privileged enrolment: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve cpak executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve cpak executable: %w", err)
	}
	tool, err := enrolmentEscalationTool()
	if err != nil {
		return err
	}
	command := exec.Command(tool, executable, "system-authority", "--enrol-request")
	command.Stdin = &input
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func enrolmentEscalationTool() (string, error) {
	candidates := enrolmentEscalationTools()
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no way to ask for administrator rights: install one of %s, or run this command as root",
		strings.Join(candidates, ", "))
}

func enrolmentEscalationTools() []string {
	graphical := []string{"pkexec", "run0"}
	terminal := []string{"sudo", "doas"}
	if os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != "" {
		return append(graphical, terminal...)
	}
	return append(terminal, graphical...)
}

// RunEnrolmentRequest applies the one request the unprivileged install could
// not put through the host transports. The command front end admits this path
// only after it has become root.
func RunEnrolmentRequest(input io.Reader) error {
	return runEnrolmentRequest(input, DefaultAnchorLedger())
}

func runEnrolmentRequest(input io.Reader, ledger AnchorLedger) error {
	message, err := decodeSocketRequest(input)
	if err != nil {
		return errors.New("invalid privileged enrolment request")
	}
	if message.Action != anchorEnrolAction {
		return errors.New("privileged enrolment request contains another action")
	}
	return applyAnchor(ledger, message)
}
