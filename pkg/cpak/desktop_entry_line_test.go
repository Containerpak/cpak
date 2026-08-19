/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import "testing"

// The spellings a launcher accepts and a byte prefix does not. Every one of
// these is an Exec line to GLib, so every one of them has to be an Exec line
// here: the rewrite is what keeps a menu launch inside the sandbox, and a line
// cpak does not recognise is left exactly as the publisher wrote it.
func TestEveryExecSpellingALauncherAcceptsIsRecognised(t *testing.T) {
	for _, line := range []string{
		"Exec=/bin/sh",
		" Exec=/bin/sh",
		"\tExec=/bin/sh",
		"Exec =/bin/sh",
		"Exec\t=/bin/sh",
		"  Exec  =  /bin/sh",
	} {
		key, value, ok := desktopEntryKey(line)
		if !ok || key != "Exec" {
			t.Fatalf("%q was not read as an Exec line: key=%q ok=%v", line, key, ok)
		}
		if value != "/bin/sh" {
			t.Fatalf("%q gave the value %q", line, value)
		}
	}
}

func TestWhatIsNotAKeyIsNotReadAsOne(t *testing.T) {
	for _, line := range []string{
		"",
		"   ",
		"# Exec=/bin/sh",
		"[Desktop Entry]",
		"  [Desktop Action open]",
		"Exec",
		"=/bin/sh",
	} {
		if key, _, ok := desktopEntryKey(line); ok {
			t.Fatalf("%q was read as the key %q", line, key)
		}
	}
}

// A locale suffix is a different key to the launcher, which reads Exec without
// one and would never run Exec[it]. Folding it in here would rewrite a line
// nothing executes and could collide with the real one.
func TestALocaleSuffixIsADifferentKey(t *testing.T) {
	key, _, ok := desktopEntryKey("Exec[it]=/bin/sh")
	if !ok || key != "Exec[it]" {
		t.Fatalf("a locale suffixed key came back as %q", key)
	}
}
