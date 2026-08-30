/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package systembroker

import (
	"testing"

	"github.com/creack/pty"
)

func TestCpakShimReadsTerminalSize(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if err := pty.Setsize(slave, &pty.Winsize{Rows: 41, Cols: 132}); err != nil {
		t.Fatal(err)
	}

	rows, columns := shimTerminalSize(slave, true)
	if rows != 41 || columns != 132 {
		t.Fatalf("cpak shim terminal size: %dx%d", columns, rows)
	}
}
