/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systemauthority

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

func Register(session Session) error {
	if err := session.Validate(); err != nil {
		return err
	}
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect system bus: %w", err)
	}
	defer connection.Close()
	call := connection.Object(BusName, ObjectPath).Call(
		InterfaceName+".RegisterSession",
		0,
		session.ID,
		session.Origin,
		session.Name,
		session.Description,
		session.Kind,
	)
	if call.Err != nil {
		return fmt.Errorf("register login session: %w", call.Err)
	}
	return nil
}

func Remove(id, origin string) error {
	if len(id) == 0 || len(id) > 96 || !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("invalid session identifier")
	}
	if err := validateOrigin(origin); err != nil {
		return err
	}
	connection, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("connect system bus: %w", err)
	}
	defer connection.Close()
	call := connection.Object(BusName, ObjectPath).Call(InterfaceName+".RemoveSession", 0, id, origin)
	if call.Err != nil {
		return fmt.Errorf("remove login session: %w", call.Err)
	}
	return nil
}
