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
	for _, command := range []string{"spawn", "network-helper", "launch", "dedup", "host-action", "system-broker-server", "system-authority", "self-update"} {
		if !skipUpdateCheck([]string{"cpak", command}) {
			t.Fatalf("internal command %s performs an update check", command)
		}
	}
	if skipUpdateCheck([]string{"cpak", "install"}) {
		t.Fatal("interactive command skipped the update check")
	}
}
