/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	ActionRegisterSession = "it.cpak.system.register-session"
	ActionRemoveSession   = "it.cpak.system.remove-session"
)

type Authorizer interface {
	Authorize(sender dbus.Sender, action string, details map[string]string) error
}

type PolkitAuthorizer struct {
	Connection *dbus.Conn
}

func (a PolkitAuthorizer) Authorize(sender dbus.Sender, action string, details map[string]string) error {
	if a.Connection == nil || sender == "" {
		return errors.New("authorization subject is unavailable")
	}
	subject := struct {
		Kind    string
		Details map[string]dbus.Variant
	}{
		Kind: "system-bus-name",
		Details: map[string]dbus.Variant{
			"name": dbus.MakeVariant(string(sender)),
		},
	}
	call := a.Connection.Object("org.freedesktop.PolicyKit1", "/org/freedesktop/PolicyKit1/Authority").Call(
		"org.freedesktop.PolicyKit1.Authority.CheckAuthorization",
		0,
		subject,
		action,
		details,
		uint32(1),
		"",
	)
	if call.Err != nil {
		return fmt.Errorf("check authorization: %w", call.Err)
	}
	var result polkitResult
	if err := call.Store(&result); err != nil {
		return fmt.Errorf("decode authorization result: %w", err)
	}
	if !result.Authorized {
		return errors.New("authorization denied")
	}
	return nil
}

// polkitResult is the one value CheckAuthorization answers with. polkit
// declares it as (bba{ss}): a single struct of three members, not three
// values. Reading it as three is a length mismatch, and it is a mismatch
// nothing sees until an authority is reachable and something actually asks it
// for permission.
type polkitResult struct {
	Authorized bool
	Challenge  bool
	Details    map[string]string
}
