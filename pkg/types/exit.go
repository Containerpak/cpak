/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package types

import "fmt"

// ExitError carries the exit status of a command cpak ran on behalf of the
// user, so that the caller can exit with the very same code instead of
// collapsing every failure into 1.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}
