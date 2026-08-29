/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
)

const slirpNameserver = "10.0.2.3"

type userNetworkPlan struct {
	path string
}

func resolveUserNetwork(enabled, hostNetwork bool) (*userNetworkPlan, error) {
	if hostNetwork {
		if !enabled {
			return nil, fmt.Errorf("host network requires network access")
		}
		return nil, nil
	}
	if !enabled {
		return nil, nil
	}
	path, err := exec.LookPath("slirp4netns")
	if err != nil {
		return nil, fmt.Errorf("network access requires slirp4netns: %w", err)
	}
	return &userNetworkPlan{path: path}, nil
}

func (p *userNetworkPlan) command(pid int, ready, exit *os.File) *exec.Cmd {
	command := exec.Command(p.path,
		"--configure",
		"--disable-host-loopback",
		"--enable-sandbox",
		"--enable-seccomp",
		"--ready-fd=3",
		"--exit-fd=4",
		strconv.Itoa(pid),
		"tap0",
	)
	command.ExtraFiles = []*os.File{ready, exit}
	return command
}

func readNetworkReady(reader io.Reader) error {
	buffer := []byte{0}
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return err
	}
	if buffer[0] != '1' {
		return fmt.Errorf("invalid network readiness response")
	}
	return nil
}
