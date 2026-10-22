/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systemauthority

import (
	"os"

	"github.com/godbus/dbus/v5"
)

const runSystemBusAddress = "unix:path=/run/dbus/system_bus_socket"

// connectSystemBus reaches the system bus where the environment or the
// default address says it is, and at /run/dbus when /var/run is not the link
// to /run the default address relies on.
func connectSystemBus() (*dbus.Conn, error) {
	connection, err := dbus.ConnectSystemBus()
	if err == nil || os.Getenv("DBUS_SYSTEM_BUS_ADDRESS") != "" {
		return connection, err
	}
	if fallback, fallbackErr := dbus.Connect(runSystemBusAddress); fallbackErr == nil {
		return fallback, nil
	}
	return nil, err
}
