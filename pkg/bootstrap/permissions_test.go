/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package bootstrap

import (
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestPermissionSummaryNamesPrivateDesktopCapabilities(t *testing.T) {
	permissions := SummarizePermissions(types.Override{DisplayX11: true, Bluetooth: true})
	if len(permissions) != 2 {
		t.Fatalf("private desktop permissions: %+v", permissions)
	}
	if permissions[0].Name != "Display" || permissions[0].Detail != "isolated X11 compatibility display" {
		t.Fatalf("X11 permission summary: %+v", permissions[0])
	}
	if permissions[1].Name != "Bluetooth" || permissions[1].Detail != "general BlueZ service through a private proxy" {
		t.Fatalf("Bluetooth permission summary: %+v", permissions[1])
	}
}

func TestPermissionSummaryDisclosesHostNetworkSharing(t *testing.T) {
	permissions := SummarizePermissions(types.Override{Network: true, HostNetwork: true})
	if len(permissions) != 2 {
		t.Fatalf("network permissions: %+v", permissions)
	}
	if permissions[1].Name != "Host network" || permissions[1].Detail != "shared network namespace, including localhost services and host ports" {
		t.Fatalf("host network permission summary: %+v", permissions[1])
	}
}
