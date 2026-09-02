//go:build js && wasm

/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Command cpak-wasm exposes the cpak Learn decision module to a page.
//
// The module is built against the release's domain types and carries the pure
// decision code used by the Learn exercises. Its CI smoke test covers the
// current manifest version, permission catalog and fail-closed network rules.
//
// It reaches nothing. There is no network, no file and no clock in here: every
// call is a function of the JSON it was handed, including the machine, which
// the caller describes rather than the module discovering it.
package main

import (
	"encoding/json"
	"syscall/js"
)

// version is set at build time. It names the cpak the module was built from,
// which is what an exam edition pins itself to.
var version = "dev"

func main() {
	api := map[string]any{
		"version":              version,
		"validateManifest":     call(validateManifest),
		"ungrantedPermissions": call(ungrantedPermissions),
		"effectivePolicy":      call(effectivePolicy),
		"filesystemPlan":       call(filesystemPlan),
		"migrateManifest":      call(migrateManifest),
		"desktopEntry":         call(desktopEntry),
		"permissionCatalog":    call(permissionCatalog),
	}
	js.Global().Set("cpak", js.ValueOf(api))

	// The exported functions live as long as the program does, and a wasm
	// program that returns is a wasm program whose exports have been freed.
	select {}
}

// call wraps one decision as a function a page can hold.
//
// Every call takes one JSON string and answers one JSON string, and it answers
// one even when it fails: a panic inside the module becomes the same refused
// response a bad request does, because a page that has to tell the difference
// between a rejection and a crash cannot be written.
func call(decide func(string) any) js.Func {
	return js.FuncOf(func(_ js.Value, arguments []js.Value) (result any) {
		defer func() {
			if recovered := recover(); recovered != nil {
				result = encode(response{OK: false, Error: describe(recovered)})
			}
		}()
		payload := ""
		if len(arguments) > 0 && arguments[0].Type() == js.TypeString {
			payload = arguments[0].String()
		}
		return encode(decide(payload))
	})
}

func encode(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `{"ok":false,"error":"the answer could not be encoded"}`
	}
	return string(encoded)
}

func describe(recovered any) string {
	if err, ok := recovered.(error); ok {
		return err.Error()
	}
	if text, ok := recovered.(string); ok {
		return text
	}
	return "the call failed"
}
