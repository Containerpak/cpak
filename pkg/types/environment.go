/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

import "time"

// Environment is a persistent, user-managed instance of an installed package.
type Environment struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	ApplicationCpakId string    `json:"application_cpak_id"`
	Origin            string    `json:"origin"`
	Version           string    `json:"version"`
	Branch            string    `json:"branch,omitempty"`
	Commit            string    `json:"commit,omitempty"`
	Release           string    `json:"release,omitempty"`
	Policy            Override  `json:"policy"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// EnvironmentProcess is a process visible inside an environment's private PID namespace.
type EnvironmentProcess struct {
	PID       int32   `json:"pid"`
	Command   string  `json:"command"`
	CPU       float64 `json:"cpu_percent"`
	Memory    uint64  `json:"memory_bytes"`
	CanSignal bool    `json:"can_signal"`
}

type EnvironmentApplicationExport struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
	IconPNG     []byte `json:"icon_png,omitempty"`
}

type EnvironmentApplicationExportState struct {
	Application string `json:"application"`
	Exported    bool   `json:"exported"`
}
