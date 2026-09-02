//go:build !(js && wasm)

/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

package main

import (
	"fmt"
	"os"
)

// The module only means anything inside a JavaScript runtime. This exists so
// the package still builds and vets everywhere else, rather than being a hole
// in go build ./... that nobody notices until the release.
func main() {
	fmt.Fprintln(os.Stderr, "cpak-wasm is built for GOOS=js GOARCH=wasm")
	os.Exit(1)
}
