/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package tools

import (
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

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

// SanitizeForDisplay makes a string safe to write to a terminal, by replacing
// every control character with the escape that spells it.
//
// A permission prompt is the one place a user is asked to grant the whole of
// /dev, the system bus or the home directory, and every string on it was
// written by the publisher of the package being installed. A cursor movement
// in one of them redraws the lines above it, so a package can show one set of
// permissions and be granted another. Names are kept intact and only what a
// terminal acts on is spelled out, so the reader still sees what was asked
// for.
//
// Both control ranges are covered. C0 is the familiar one, ESC included; C1,
// which a UTF-8 terminal reads from U+0080 to U+009F, contains the
// single-character forms of the same sequences, and U+009B is the CSI that
// "ESC [" spells in two bytes.
//
// A byte no decoder accepts is spelled out too. It is not text, and a terminal
// reading anything other than UTF-8 acts on a lone 0x9b as the CSI it is in
// that encoding.
func SanitizeForDisplay(value string) string {
	if !strings.ContainsFunc(value, isControlCharacter) && utf8.ValidString(value) {
		return value
	}
	var safe strings.Builder
	safe.Grow(len(value))
	for index := 0; index < len(value); {
		character, width := utf8.DecodeRuneInString(value[index:])
		switch {
		case character == utf8.RuneError && width == 1:
			fmt.Fprintf(&safe, "\\x%02x", value[index])
		case !isControlCharacter(character):
			safe.WriteRune(character)
		case character < 0x80:
			fmt.Fprintf(&safe, "\\x%02x", character)
		default:
			fmt.Fprintf(&safe, "\\u%04x", character)
		}
		index += width
	}
	return safe.String()
}

func isControlCharacter(character rune) bool {
	return character < 0x20 || character == 0x7f || (character >= 0x80 && character <= 0x9f)
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
		// This is where a whole permission set reaches the terminal, values
		// and all, so nothing goes out with the cursor movements still in it.
		if field.Kind() == reflect.String {
			logger.Printf("  - %s: %s", snakeCaseName, SanitizeForDisplay(field.String()))
			continue
		}
		if field.Kind() == reflect.Slice {
			logger.Printf("  - %s:", snakeCaseName)
			for j := 0; j < field.Len(); j++ {
				logger.Printf("    - %s", SanitizeForDisplay(fmt.Sprintf("%v", field.Index(j).Interface())))
			}
			continue
		}
		if field.Kind() == reflect.Map {
			logger.Printf("  - %s:", snakeCaseName)
			for _, key := range field.MapKeys() {
				logger.Printf("    - %s: %s", SanitizeForDisplay(key.String()), SanitizeForDisplay(field.MapIndex(key).String()))
			}
			continue
		}
		if field.Kind() == reflect.Bool {
			logger.Printf("  - %s: %v", snakeCaseName, field.Bool())
			continue
		}
		logger.Printf("  - %s: %s", snakeCaseName, SanitizeForDisplay(fmt.Sprintf("%v", field.Interface())))
	}
}
