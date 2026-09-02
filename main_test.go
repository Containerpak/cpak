/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package main

import "testing"

func TestInternalCommandsSkipUpdateCheck(t *testing.T) {
	previous := version
	version = "v2.0.0"
	defer func() { version = previous }()
	for _, command := range []string{"spawn", "network-helper", "x11-broker", "launch", "dedup", "host-action", "system-broker-server", "system-authority", "self-update", "service", "ps", "status", "inspect", "health"} {
		if !skipUpdateCheck([]string{"cpak", command}) {
			t.Fatalf("internal command %s performs an update check", command)
		}
	}
	if skipUpdateCheck([]string{"cpak", "install"}) {
		t.Fatal("interactive command skipped the update check")
	}
	if !skipUpdateCheck([]string{"cpak", "run", "--instance", "service-api", "github.com/example/api"}) {
		t.Fatal("managed service launch performs an update check")
	}
}
