/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestNestedOverrideValuePreservesTheRestOfThePolicy(t *testing.T) {
	override := types.Override{
		Network: true,
		SessionBus: types.DBusPolicy{
			Talk: []types.DBusCallGrant{{Name: "org.example.Service", Path: "/org/example", Interface: "org.example.Service", Members: []string{"Ping"}}},
		},
	}
	value := `["com.steampowered.PressureVessel.LaunchAlongsideSteam"]`
	if err := applyOverrideJSONValue(&override, "sessionBus.own", []byte(value)); err != nil {
		t.Fatal(err)
	}
	if !override.Network || len(override.SessionBus.Talk) != 1 {
		t.Fatalf("unrelated policy changed: %+v", override)
	}
	if want := []string{"com.steampowered.PressureVessel.LaunchAlongsideSteam"}; !reflect.DeepEqual(override.SessionBus.Own, want) {
		t.Fatalf("session bus names: got %v, want %v", override.SessionBus.Own, want)
	}
}

func TestOverrideJSONValueAcceptsColonSeparatedEnvironmentValues(t *testing.T) {
	var override types.Override
	value := `["DATABASE_URL=postgres://database:5432/app"]`
	if err := applyOverrideJSONValue(&override, "env", []byte(value)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(override.Env, []string{"DATABASE_URL=postgres://database:5432/app"}) {
		t.Fatalf("environment: %v", override.Env)
	}
}

func TestOverrideJSONValueRejectsUnknownPathsAndExtraValues(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "sessionBus.unknown", value: `true`},
		{key: "network.enabled", value: `true`},
		{key: "sessionBus.own", value: `[] {}`},
	} {
		var override types.Override
		if err := applyOverrideJSONValue(&override, test.key, []byte(test.value)); err == nil {
			t.Fatalf("accepted %s=%s", test.key, test.value)
		}
	}
}

func TestValidateOverrideRejectsInvalidNestedPolicies(t *testing.T) {
	override := types.Override{SessionBus: types.DBusPolicy{Own: []string{"not-a-bus-name"}}}
	if err := validateOverride(override); err == nil {
		t.Fatal("accepted an invalid session bus name")
	}
}

func TestEditOverrideReadsTheValidatedEditorResult(t *testing.T) {
	directory := t.TempDir()
	editor := filepath.Join(directory, "editor")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"network\":true,\"env\":[\"URL=https://example.com:8443\"]}' >\"$1\"\n"
	if err := os.WriteFile(editor, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", editor)
	t.Setenv("EDITOR", "")
	override, err := editOverride(types.Override{})
	if err != nil {
		t.Fatal(err)
	}
	if !override.Network || !reflect.DeepEqual(override.Env, []string{"URL=https://example.com:8443"}) {
		t.Fatalf("edited override: %+v", override)
	}
}

func TestDecodeOverrideJSONRejectsUnknownFieldsAndMultipleValues(t *testing.T) {
	for _, data := range []string{`{"unknown":true}`, `{} {}`} {
		if _, err := decodeOverrideJSON([]byte(data)); err == nil {
			t.Fatalf("accepted invalid override: %s", data)
		}
	}
}

func TestEditOverrideRequiresAnEditor(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	_, err := editOverride(types.Override{})
	if err == nil || !strings.Contains(err.Error(), "VISUAL or EDITOR") {
		t.Fatalf("missing editor: %v", err)
	}
}
