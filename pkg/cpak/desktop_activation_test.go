/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"strings"
	"testing"
)

func TestDesktopActivationTokenRequiresDesktopLaunch(t *testing.T) {
	cp := Cpak{}
	if err := cp.SetDesktopActivationToken("activation-token"); err == nil {
		t.Fatal("activation token was accepted outside a desktop launch")
	}
}

func TestDesktopActivationTokenRejectsInvalidValues(t *testing.T) {
	cp := Cpak{}
	cp.SetDesktopLaunch(true)
	for _, token := range []string{"bad\ntoken", strings.Repeat("t", 4097)} {
		if err := cp.SetDesktopActivationToken(token); err == nil {
			t.Fatalf("invalid activation token %q was accepted", token)
		}
	}
}

func TestDesktopActivationTokenIsInvocationOnly(t *testing.T) {
	cp := Cpak{}
	cp.SetDesktopLaunch(true)
	if err := cp.SetDesktopActivationToken("activation-token"); err != nil {
		t.Fatal(err)
	}

	identity, err := cp.runtimeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if identity != "" {
		t.Fatalf("activation token changed runtime identity: %q", identity)
	}
	environment := cp.applyDesktopActivationToken([]string{
		"PATH=/usr/bin",
		"XDG_ACTIVATION_TOKEN=stale",
	})
	if got := environmentValue(environment, "XDG_ACTIVATION_TOKEN"); got != "activation-token" {
		t.Fatalf("activation token: got %q", got)
	}
}

func TestDesktopLaunchClearsAnUntrustedActivationToken(t *testing.T) {
	cp := Cpak{}
	cp.SetDesktopLaunch(true)
	environment := cp.applyDesktopActivationToken([]string{
		"XDG_ACTIVATION_TOKEN=package-value",
	})
	if hasEnvironmentName(environment, "XDG_ACTIVATION_TOKEN") {
		t.Fatal("untrusted activation token was retained")
	}
}

func TestNonDesktopLaunchClearsTheHostActivationToken(t *testing.T) {
	cp := Cpak{}
	environment := cp.applyDesktopActivationToken([]string{
		"XDG_ACTIVATION_TOKEN=host-value",
	})
	if hasEnvironmentName(environment, "XDG_ACTIVATION_TOKEN") {
		t.Fatal("host activation token escaped into a non-desktop launch")
	}
}

func hasEnvironmentName(environment []string, name string) bool {
	prefix := name + "="
	for _, variable := range environment {
		if strings.HasPrefix(variable, prefix) {
			return true
		}
	}
	return false
}
