/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package types

// HostExecRequest defines the structure for requesting command execution on the host.
type HostExecRequest struct {
	// CommandAndArgs is the command and its arguments to be executed.
	CommandAndArgs []string `json:"command"`
	// Width is the initial terminal width for PTY setup.
	Width uint16 `json:"width"`
	// Height is the initial terminal height for PTY setup.
	Height uint16 `json:"height"`
}
