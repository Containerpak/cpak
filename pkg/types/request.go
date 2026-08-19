/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package types

type RequestParams struct {
	Action string `json:"action"`
	// Token is the capability the calling container was given when it was
	// built. The parent it speaks for is resolved from it, never read from the
	// message.
	Token     string   `json:"token"`
	Origin    string   `json:"origin"`
	Version   string   `json:"version"`
	Branch    string   `json:"branch"`
	Commit    string   `json:"commit"`
	Release   string   `json:"release"`
	Binary    string   `json:"binary"`
	ExtraArgs []string `json:"extraArgs"`
}
