/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

// polkit declares CheckAuthorization as answering (bba{ss}): one struct, not
// three values. Reading it as three decodes nothing and refuses every
// enrolment, and it is invisible until an authority is reachable and something
// asks it for permission, which is why it survived to a running host.
func TestTheAuthorizationResultIsOneStructAndNotThreeValues(t *testing.T) {
	answered := []interface{}{polkitResult{Authorized: true, Details: map[string]string{}}}

	var result polkitResult
	if err := dbus.Store(answered, &result); err != nil {
		t.Fatalf("the reply polkit sends cannot be decoded: %v", err)
	}
	if !result.Authorized {
		t.Fatal("an authorised reply decoded as a refusal")
	}

	// The shape this used to have, kept as the thing that must not work again.
	var authorized, challenge bool
	var details map[string]string
	if err := dbus.Store(answered, &authorized, &challenge, &details); err == nil {
		t.Fatal("three separate values decoded a reply that is one struct")
	}
}

// A refusal has to read as a refusal and not as a decoding failure, because
// the two lead a user to look in completely different places.
func TestARefusalDecodesAsARefusal(t *testing.T) {
	answered := []interface{}{polkitResult{Authorized: false, Challenge: true, Details: map[string]string{}}}
	var result polkitResult
	if err := dbus.Store(answered, &result); err != nil {
		t.Fatal(err)
	}
	if result.Authorized {
		t.Fatal("a refusal decoded as permission")
	}
	if !result.Challenge {
		t.Fatal("the challenge flag was lost")
	}
}
