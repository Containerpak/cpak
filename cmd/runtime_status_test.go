/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"testing"

	"github.com/mirkobrombin/cpak/pkg/cpak"
)

func TestDisplayNetworkIncludesListeningPorts(t *testing.T) {
	status := cpak.RuntimeStatus{Network: "host", Ports: []int{3000, 8443}}
	if got := displayNetwork(status); got != "host:3000,8443" {
		t.Fatalf("network: %s", got)
	}
}
