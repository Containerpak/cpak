/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package core

import (
	"reflect"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/types"
)

// Permission is one entry of the permission model, read off the type that
// defines it rather than written down a second time. A list maintained by hand
// beside the struct is a list that goes stale the first time a permission is
// added, and a reader who is told the wrong set of permissions has been taught
// something false.
type Permission struct {
	// Key is the name the permission has in a manifest.
	Key string `json:"key"`

	// Field is the name it has in the runtime.
	Field string `json:"field"`

	// Kind is bool for a permission that is on or off, and the Go type
	// otherwise.
	Kind string `json:"kind"`

	// Description is the one line the manifest schema shows an author.
	Description string `json:"description"`

	// Stated is true for the permissions a manifest is expected to write down.
	// The rest are absent by design when they are empty.
	Stated bool `json:"stated"`

	// ManifestV3 is true when the permission is part of the current manifest.
	ManifestV3 bool `json:"manifestV3"`
}

// PermissionCatalog answers the permission model as it stands in this build.
func PermissionCatalog() []Permission {
	fields := reflect.TypeOf(types.Override{})
	catalog := make([]Permission, 0, fields.NumField())
	for index := 0; index < fields.NumField(); index++ {
		field := fields.Field(index)
		key, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if key == "" || key == "-" {
			continue
		}
		catalog = append(catalog, Permission{
			Key:         key,
			Field:       field.Name,
			Kind:        field.Type.String(),
			Description: schemaDescription(field.Tag.Get("jsonschema")),
			Stated:      field.Type.Kind() == reflect.Bool && !strings.Contains(options, "omitempty") && !manifestV3RemovedPermission(key),
			ManifestV3:  manifestV3Permission(key),
		})
	}
	return catalog
}

func manifestV3Permission(key string) bool {
	if manifestV3RemovedPermission(key) {
		return false
	}
	switch key {
	case "fsHost", "fsHostEtc", "fsHostHome", "fsExtra", "allowedHostCommands":
		return false
	default:
		return true
	}
}

func schemaDescription(tag string) string {
	for _, option := range strings.Split(tag, ",") {
		if value, found := strings.CutPrefix(option, "description="); found {
			return value
		}
	}
	return ""
}
