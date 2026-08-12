/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
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
	var authorized bool
	var challenge bool
	var resultDetails map[string]string
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
	if err := call.Store(&authorized, &challenge, &resultDetails); err != nil {
		return fmt.Errorf("decode authorization result: %w", err)
	}
	if !authorized {
		return errors.New("authorization denied")
	}
	return nil
}
