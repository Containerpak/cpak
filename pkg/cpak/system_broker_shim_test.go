/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"strings"
	"testing"
)

func TestRenderSystemBrokerShimUsesOnlyTypedHostActions(t *testing.T) {
	content, err := RenderSystemBrokerShim()
	if err != nil {
		t.Fatal(err)
	}
	shim := string(content)
	if !strings.Contains(shim, "host-action") {
		t.Fatal("system broker shim does not call the typed host action client")
	}
	if strings.Contains(shim, "hostexec-client") || strings.Contains(shim, "system-broker-client --operation") {
		t.Fatal("system broker shim exposes the host command bridge")
	}
}
