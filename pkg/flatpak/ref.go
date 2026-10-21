/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package flatpak

import (
	"fmt"
	"runtime"
	"strings"
)

// Kind tells an application ref from a runtime ref.
type Kind string

const (
	KindApp     Kind = "app"
	KindRuntime Kind = "runtime"
)

// Ref names one build of an application or runtime: kind/id/arch/branch.
type Ref struct {
	Kind   Kind
	ID     string
	Arch   string
	Branch string
}

func (r Ref) String() string {
	return string(r.Kind) + "/" + r.ID + "/" + r.Arch + "/" + r.Branch
}

// ParseRef reads a full ref.
func ParseRef(value string) (Ref, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 4 {
		return Ref{}, fmt.Errorf("flatpak: %q is not a kind/id/arch/branch ref", value)
	}
	ref := Ref{Kind: Kind(parts[0]), ID: parts[1], Arch: parts[2], Branch: parts[3]}
	if ref.Kind != KindApp && ref.Kind != KindRuntime {
		return Ref{}, fmt.Errorf("flatpak: unknown ref kind %q", parts[0])
	}
	if err := ref.validate(); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

// ParseRuntime reads the id/arch/branch form an application names its runtime
// and SDK in.
func ParseRuntime(value string) (Ref, error) {
	return ParseRef("runtime/" + value)
}

func (r Ref) validate() error {
	if err := ValidateID(r.ID); err != nil {
		return err
	}
	if !validPart(r.Arch) {
		return fmt.Errorf("flatpak: invalid architecture %q", r.Arch)
	}
	if !validPart(r.Branch) {
		return fmt.Errorf("flatpak: invalid branch %q", r.Branch)
	}
	return nil
}

func validPart(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		case char == '_' || char == '-' || char == '.':
		default:
			return false
		}
	}
	return value != "." && value != ".."
}

// ValidateID holds an application id to the D-Bus well-known name rules
// Flatpak itself enforces: at least three dot-separated elements of letters,
// digits, underscores and dashes, none starting with a digit. It is checked
// here because the id ends up in paths, origins and desktop file names.
func ValidateID(id string) error {
	if len(id) > 255 {
		return fmt.Errorf("flatpak: id %q is too long", id)
	}
	elements := strings.Split(id, ".")
	if len(elements) < 3 {
		return fmt.Errorf("flatpak: id %q needs at least three elements", id)
	}
	for _, element := range elements {
		if element == "" {
			return fmt.Errorf("flatpak: id %q has an empty element", id)
		}
		if element[0] >= '0' && element[0] <= '9' {
			return fmt.Errorf("flatpak: id %q has an element starting with a digit", id)
		}
		for _, char := range element {
			switch {
			case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
			case char == '_' || char == '-':
			default:
				return fmt.Errorf("flatpak: id %q holds %q", id, char)
			}
		}
	}
	return nil
}

// HostArch answers with the Flatpak name of the architecture cpak runs on.
func HostArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "i386"
	case "arm":
		return "arm"
	}
	return runtime.GOARCH
}
