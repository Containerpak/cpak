/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"testing"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestCpakHostActionUsesRawTerminalInput(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()

	restore, err := prepareHostActionTerminal("cpak-host", slave, true)
	if err != nil {
		t.Fatal(err)
	}
	state, err := unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if state.Lflag&(unix.ECHO|unix.ICANON) != 0 {
		t.Fatal("cpak host action terminal input remained canonical or echoed")
	}
	restore()

	state, err = unix.IoctlGetTermios(int(slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if state.Lflag&(unix.ECHO|unix.ICANON) != unix.ECHO|unix.ICANON {
		t.Fatal("cpak host action did not restore terminal input")
	}
}
