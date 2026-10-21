/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */

// Package flatpak reads what a Flatpak remote publishes about an application
// (refs, .flatpakref and .flatpakrepo files, the metadata keyfile and its
// extension points) and translates the permissions it asks for into a cpak
// policy. Nothing here runs Flatpak or follows its rules: an application is
// granted what cpak grants, and the translation says what was left out.
package flatpak

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

// KeyFile is a GLib key file: named groups of key=value lines, the format of
// every Flatpak description file.
type KeyFile struct {
	groups map[string]map[string]string
	order  []string
}

const maxKeyFile = 1 << 20

// ParseKeyFile reads a key file. Localised keys such as Name[it] are kept
// under their full name, so they never shadow the plain key.
func ParseKeyFile(data []byte) (*KeyFile, error) {
	if len(data) > maxKeyFile {
		return nil, fmt.Errorf("flatpak: key file exceeds %d bytes", maxKeyFile)
	}
	file := &KeyFile{groups: map[string]map[string]string{}}
	var group map[string]string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), maxKeyFile)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(text, "[") {
			if !strings.HasSuffix(text, "]") {
				return nil, fmt.Errorf("flatpak: line %d: malformed group header", line)
			}
			name := text[1 : len(text)-1]
			if name == "" || strings.ContainsAny(name, "[]") {
				return nil, fmt.Errorf("flatpak: line %d: invalid group name %q", line, name)
			}
			if _, seen := file.groups[name]; seen {
				return nil, fmt.Errorf("flatpak: line %d: group %q appears twice", line, name)
			}
			group = map[string]string{}
			file.groups[name] = group
			file.order = append(file.order, name)
			continue
		}
		if group == nil {
			return nil, fmt.Errorf("flatpak: line %d: key outside of a group", line)
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("flatpak: line %d: expected key=value", line)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("flatpak: line %d: empty key", line)
		}
		group[key] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("flatpak: read key file: %w", err)
	}
	return file, nil
}

// Groups answers with the group names in the order they appear.
func (k *KeyFile) Groups() []string {
	return append([]string{}, k.order...)
}

// Keys answers with the keys of a group.
func (k *KeyFile) Keys(group string) map[string]string {
	values := make(map[string]string, len(k.groups[group]))
	for key, value := range k.groups[group] {
		values[key] = unescape(value)
	}
	return values
}

// String answers with a value, its escapes resolved.
func (k *KeyFile) String(group, key string) string {
	return unescape(k.groups[group][key])
}

// Has reports whether a group holds a key.
func (k *KeyFile) Has(group, key string) bool {
	_, ok := k.groups[group][key]
	return ok
}

// Bool reads a boolean value the way GLib does: only true and false.
func (k *KeyFile) Bool(group, key string) bool {
	return k.groups[group][key] == "true"
}

// List reads a semicolon-separated list, where \; is a literal semicolon.
func (k *KeyFile) List(group, key string) []string {
	raw, ok := k.groups[group][key]
	if !ok {
		return nil
	}
	var values []string
	var current strings.Builder
	for index := 0; index < len(raw); index++ {
		switch {
		case raw[index] == '\\' && index+1 < len(raw):
			current.WriteByte(raw[index])
			current.WriteByte(raw[index+1])
			index++
		case raw[index] == ';':
			values = append(values, unescape(current.String()))
			current.Reset()
		default:
			current.WriteByte(raw[index])
		}
	}
	if current.Len() > 0 {
		values = append(values, unescape(current.String()))
	}
	filtered := values[:0]
	for _, value := range values {
		if value != "" {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func unescape(value string) string {
	if !strings.Contains(value, `\`) {
		return value
	}
	var out strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' || index+1 == len(value) {
			out.WriteByte(value[index])
			continue
		}
		index++
		switch value[index] {
		case 's':
			out.WriteByte(' ')
		case 'n':
			out.WriteByte('\n')
		case 't':
			out.WriteByte('\t')
		case 'r':
			out.WriteByte('\r')
		default:
			out.WriteByte(value[index])
		}
	}
	return out.String()
}
