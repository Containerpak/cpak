/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"errors"
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
)

// ErrNoAuthority reports that no transport could carry the request, which lets
// the caller escalate the single privileged step instead of failing.
var ErrNoAuthority = errors.New("no system authority is reachable")

func Register(session Session) error {
	if err := session.Validate(); err != nil {
		return err
	}
	return dispatch(socketRequest{
		Action:      "register",
		ID:          session.ID,
		Origin:      session.Origin,
		Name:        session.Name,
		Description: session.Description,
		Kind:        session.Kind,
	})
}

func Remove(id, origin string) error {
	if len(id) == 0 || len(id) > 96 || !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("invalid session identifier")
	}
	if err := validateOrigin(origin); err != nil {
		return err
	}
	return dispatch(socketRequest{Action: "remove", ID: id, Origin: origin})
}

// dispatch walks the transports in order of how much they demand from the host.
// Root already holds the privilege the authority exists to lend, so it changes
// the registry itself and needs neither a bus nor a running daemon. A transport
// that answered is final: a denial is never retried on another one.
func dispatch(message socketRequest) error {
	if os.Geteuid() == 0 {
		return applyLocally(message)
	}
	if err := retryPastStale(func() error { return requestOverBus(message) }); !errors.Is(err, errTransportUnavailable) {
		return err
	}
	if err := requestOverSocket(DefaultSocketPath, message); !errors.Is(err, errTransportUnavailable) {
		return err
	}
	return ErrNoAuthority
}

// retryPastStale runs a call that may meet an authority the host has since
// replaced. That one refusal means the service stepped aside, so asking again
// reaches the one on disk. It is the only error worth repeating: everything
// else is an answer.
func retryPastStale(call func() error) error {
	err := call()
	if !staleOnBus(err) {
		return err
	}
	return call()
}

func applyLocally(message socketRequest) error {
	registry := DefaultRegistry()
	switch message.Action {
	case "register":
		return registry.Register(Session{
			ID:          message.ID,
			Origin:      message.Origin,
			Name:        message.Name,
			Description: message.Description,
			Kind:        message.Kind,
		})
	case "remove":
		return registry.Remove(message.ID, message.Origin)
	default:
		return errors.New("unsupported system authority action")
	}
}

func requestOverBus(message socketRequest) error {
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return errTransportUnavailable
	}
	defer connection.Close()
	object := connection.Object(BusName, ObjectPath)
	var call *dbus.Call
	switch message.Action {
	case "register":
		call = object.Call(InterfaceName+".RegisterSession", 0,
			message.ID, message.Origin, message.Name, message.Description, message.Kind)
	case "remove":
		call = object.Call(InterfaceName+".RemoveSession", 0, message.ID, message.Origin)
	default:
		return errors.New("unsupported system authority action")
	}
	if call.Err == nil {
		return nil
	}
	// A bus that cannot produce the authority is a transport that failed, not a
	// refusal, so the caller is free to try the socket.
	if unreachableOnBus(call.Err) {
		return errTransportUnavailable
	}
	if message.Action == "register" {
		return fmt.Errorf("register login session: %w", call.Err)
	}
	return fmt.Errorf("remove login session: %w", call.Err)
}

func unreachableOnBus(err error) bool {
	var busErr dbus.Error
	if !errors.As(err, &busErr) {
		return false
	}
	switch busErr.Name {
	case "org.freedesktop.DBus.Error.ServiceUnknown",
		"org.freedesktop.DBus.Error.NameHasNoOwner",
		"org.freedesktop.DBus.Error.Spawn.ExecFailed",
		"org.freedesktop.DBus.Error.NoReply":
		return true
	}
	return false
}
