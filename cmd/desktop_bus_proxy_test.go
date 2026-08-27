/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDesktopBusPolicyDecoderRejectsTrailingData(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(`{} {}`))
	if _, err := decodeDesktopBusPolicy(encoded); err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("got %v, want trailing data to be rejected", err)
	}
}
