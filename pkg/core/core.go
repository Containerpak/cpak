/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Package core contains the pure decisions used by the cpak Learn exercises.
//
// Every fact arrives as an argument. The package starts no process, opens no
// file, reads no environment and reaches no network while it answers. This
// keeps the teaching module deterministic and lets CI exercise it against a
// machine that does not have to exist.
package core
