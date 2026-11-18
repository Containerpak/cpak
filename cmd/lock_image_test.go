/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import "testing"

func TestLockImageRequiresOneBuildSource(t *testing.T) {
	for _, command := range []LockImageCmd{
		{},
		{Image: "example.invalid/app:latest", Packages: []string{"hello"}},
		{Registry: "nixery.dev"},
		{Packages: []string{"hello"}, Architecture: "unknown"},
		{Image: "invalid/image@tag"},
	} {
		if err := command.Run(); err == nil {
			t.Fatalf("invalid build source accepted: %+v", command)
		}
	}
}
