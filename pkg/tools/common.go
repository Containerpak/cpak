/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package tools

import (
	"fmt"
	"reflect"
	"strings"
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
			fmt.Printf("  - %s: %s\n", snakeCaseName, field.String())
			continue
		}
		if field.Kind() == reflect.Slice {
			fmt.Printf("  - %s:\n", snakeCaseName)
			for j := 0; j < field.Len(); j++ {
				fmt.Printf("    - %s\n", field.Index(j).String())
			}
			continue
		}
		if field.Kind() == reflect.Map {
			fmt.Printf("  - %s:\n", snakeCaseName)
			for _, key := range field.MapKeys() {
				fmt.Printf("    - %s: %s\n", key.String(), field.MapIndex(key).String())
			}
			continue
		}
		if field.Kind() == reflect.Bool {
			fmt.Printf("  - %s: %v\n", snakeCaseName, field.Bool())
			continue
		}
		fmt.Printf("  - %s: %v\n", snakeCaseName, field.Interface())
	}
}
