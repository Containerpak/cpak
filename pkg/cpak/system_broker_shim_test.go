/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"strings"
	"testing"
)

func TestRenderSystemBrokerShimUsesOnlyBrokerClient(t *testing.T) {
	content, err := RenderSystemBrokerShim()
	if err != nil {
		t.Fatal(err)
	}
	shim := string(content)
	if !strings.Contains(shim, "system-broker-client") {
		t.Fatal("system broker shim does not call the broker client")
	}
	if strings.Contains(shim, "hostexec-client") {
		t.Fatal("system broker shim exposes the host command bridge")
	}
}
