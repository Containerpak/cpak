/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/core"
	"github.com/mirkobrombin/cpak/pkg/integrity"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestTrayPolicyNeedsAnExplicitGrantAtEveryLevel(t *testing.T) {
	enabled := types.Override{DisplayX11: true, TrayProxy: "xembed"}
	disabled := types.Override{DisplayX11: true}
	for _, intersect := range []func(types.Override, types.Override) types.Override{
		intersectOverrides,
		func(parent, child types.Override) types.Override { return core.Intersect(parent, child, core.Host{}) },
	} {
		if intersect(enabled, enabled).TrayProxy != "xembed" {
			t.Fatal("matching tray grants were removed")
		}
		if intersect(enabled, disabled).TrayProxy != "" || intersect(disabled, enabled).TrayProxy != "" {
			t.Fatal("tray proxy escaped the permission ceiling")
		}
	}
	if integrity.Restricts(disabled, enabled) {
		t.Fatal("enabling host tray presentation did not require consent")
	}
	if !integrity.Restricts(enabled, disabled) {
		t.Fatal("removing tray presentation required new consent")
	}
	data, err := json.Marshal(disabled)
	if err != nil || strings.Contains(string(data), "trayProxy") {
		t.Fatalf("disabled tray changed the old policy encoding: %s %v", data, err)
	}
}

func TestTrayPolicyRejectsUnknownModesAndMissingPrivateDisplay(t *testing.T) {
	if types.ValidateTrayProxy("", false) != nil || types.ValidateTrayProxy("xembed", true) != nil {
		t.Fatal("supported tray mode was rejected")
	}
	if types.ValidateTrayProxy("xembed", false) == nil || types.ValidateTrayProxy("raw", true) == nil {
		t.Fatal("invalid tray configuration was accepted")
	}
	if validateManifestBusPolicy("3.0", "test", types.Override{TrayProxy: "xembed"}) == nil {
		t.Fatal("manifest accepted a tray without a private display")
	}
}
