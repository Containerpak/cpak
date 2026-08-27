/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

const slirpNameserver = "10.0.2.3"

type userNetworkPlan struct {
	path string
}

func resolveUserNetwork(enabled bool) (*userNetworkPlan, error) {
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
