/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import "testing"

func TestDesktopActivationTokenIsLimitedToDesktopLaunches(t *testing.T) {
	t.Setenv("XDG_ACTIVATION_TOKEN", "activation-token")
	if got := desktopActivationToken(false); got != "" {
		t.Fatalf("non-desktop activation token: got %q", got)
	}
	if got := desktopActivationToken(true); got != "activation-token" {
		t.Fatalf("desktop activation token: got %q", got)
	}
}
