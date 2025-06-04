/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package tools

import (
	"reflect"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/logger"
)

// CamelToSnake converts a camel case string to a snake case string
func CamelToSnake(name string) string {
	var result strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteByte('-')
		}
		result.WriteRune(r)
	}
	return strings.ToLower(result.String())
}

// PrintStructKeyVal prints the key-value pairs of a struct into a
// human-readable format
func PrintStructKeyVal(structure interface{}) {
	val := reflect.ValueOf(structure)
	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		name := typ.Field(i).Name
		snakeCaseName := CamelToSnake(name)
		if field.Kind() == reflect.String {
			logger.Printf("  - %s: %s", snakeCaseName, field.String())
			continue
		}
		if field.Kind() == reflect.Slice {
			logger.Printf("  - %s:", snakeCaseName)
			for j := 0; j < field.Len(); j++ {
				logger.Printf("    - %s", field.Index(j).String())
			}
			continue
		}
		if field.Kind() == reflect.Map {
			logger.Printf("  - %s:", snakeCaseName)
			for _, key := range field.MapKeys() {
				logger.Printf("    - %s: %s", key.String(), field.MapIndex(key).String())
			}
			continue
		}
		if field.Kind() == reflect.Bool {
			logger.Printf("  - %s: %v", snakeCaseName, field.Bool())
			continue
		}
		logger.Printf("  - %s: %v", snakeCaseName, field.Interface())
	}
}
