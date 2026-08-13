/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package main

import "testing"

func TestInternalCommandsSkipUpdateCheck(t *testing.T) {
	previous := version
	version = "v2.0.0"
	defer func() { version = previous }()
	for _, command := range []string{"spawn", "launch", "dedup", "host-action", "system-broker-server", "system-authority", "self-update"} {
		if !skipUpdateCheck([]string{"cpak", command}) {
			t.Fatalf("internal command %s performs an update check", command)
		}
	}
	if skipUpdateCheck([]string{"cpak", "install"}) {
		t.Fatal("interactive command skipped the update check")
	}
}
