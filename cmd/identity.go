/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"os"
	"strconv"
	"strings"
)

const containerIdentity = "1000"

var fallbackShells = []string{"/bin/bash", "/bin/sh"}

func outsideIdentity(mapPath string) (string, bool) {
	content, err := os.ReadFile(mapPath)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "0" {
			if _, err := strconv.ParseUint(fields[1], 10, 32); err == nil {
				return fields[1], true
			}
		}
	}
	return "", false
}

func containerPasswd(content []byte, hostUID string, shellExists func(string) bool) []byte {
	return rewriteIdentity(content, hostUID, 7, func(fields []string) {
		fields[3] = containerIdentity
		if shellExists(fields[6]) {
			return
		}
		for _, shell := range fallbackShells {
			if shellExists(shell) {
				fields[6] = shell
				return
			}
		}
	})
}

func containerGroup(content []byte, hostGID string) []byte {
	return rewriteIdentity(content, hostGID, 4, func([]string) {})
}

func rewriteIdentity(content []byte, hostID string, width int, adjust func([]string)) []byte {
	lines := strings.Split(string(content), "\n")
	name := ""
	for _, line := range lines {
		fields := strings.Split(line, ":")
		if len(fields) == width && fields[2] == hostID {
			name = fields[0]
			break
		}
	}
	if name == "" {
		return content
	}
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, ":")
		if len(fields) != width {
			kept = append(kept, line)
			continue
		}
		if fields[0] == name {
			if fields[2] != hostID {
				continue
			}
			fields[2] = containerIdentity
			adjust(fields)
			kept = append(kept, strings.Join(fields, ":"))
			continue
		}
		if fields[2] == containerIdentity {
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, "\n"))
}
