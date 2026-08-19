/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package tools

import (
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Every character these tests are about is written as an escape rather than as
// itself. They are exactly the characters an editor, a diff or a paste eats or
// normalises without saying so, and a test of them that cannot survive being
// copied is not a test of them.

func TestSanitizeForDisplayEscapesEveryControlCharacter(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		value string
		want  string
	}{
		{"ordinary text is left alone", "network access", "network access"},
		{"text above the control ranges is left alone", "caff\u00e8 \u00e0 \u4e2d", "caff\u00e8 \u00e0 \u4e2d"},
		{"the escape byte", "a\x1b[1Ab", `a\x1b[1Ab`},
		{"a newline", "a\nb", `a\x0ab`},
		{"a carriage return", "a\rb", `a\x0db`},
		{"a tab", "a\tb", `a\x09b`},
		{"a null byte", "a\x00b", `a\x00b`},
		{"delete", "a\x7fb", `a\x7fb`},
		{"the single character CSI, which is what ESC [ spells", "a\u009b1Ab", `a\u009b1Ab`},
		{"the single character CSI redrawing a line", "\u009b2K", `\u009b2K`},
		{"the single character OSC, which retitles the window", "a\u009d0;title\u009cb", `a\u009d0;title\u009cb`},
		{"a byte no decoder accepts", "a\x9bb", `a\x9bb`},
	} {
		if got := SanitizeForDisplay(testCase.value); got != testCase.want {
			t.Errorf("%s: got %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

// The predicate is the whole of the defence, so it is held to the ranges
// themselves rather than to a handful of examples: C0, DEL, and the whole of
// C1, which is the half a terminal acts on with no ESC in front of it and the
// half an escape set that stops at 0x7f leaves untouched.
func TestSanitizeForDisplayCoversBothControlRanges(t *testing.T) {
	for character := rune(0); character <= 0x9f; character++ {
		value := string(character)
		got := SanitizeForDisplay(value)

		switch {
		case character <= 0x1f, character == 0x7f:
			want := fmt.Sprintf(`\x%02x`, character)
			if got != want {
				t.Errorf("U+%04X: got %q, want %q", character, got, want)
			}
		case character >= 0x80:
			want := fmt.Sprintf(`\u%04x`, character)
			if got != want {
				t.Errorf("U+%04X: got %q, want %q", character, got, want)
			}
		default:
			if got != value {
				t.Errorf("U+%04X is printable and was escaped to %q", character, got)
			}
		}
	}

	// The characters immediately outside the two ranges, since an off-by-one
	// at either end is how this rule goes wrong: a space is the first
	// printable one, a non-breaking space is the first one above C1.
	for _, character := range []rune{0x20, 0x7e, 0x00a0, 0x2588} {
		if got := SanitizeForDisplay(string(character)); got != string(character) {
			t.Errorf("U+%04X is not a control character and was escaped to %q", character, got)
		}
	}
}

// The prompt where a user grants the whole of /dev, the system bus and the home
// directory prints values a publisher wrote. A cursor movement in one of them
// redraws the lines above it, so the permissions on the screen stop being the
// permissions being granted.
func TestPrintStructKeyValLetsNoPublisherValueMoveTheCursor(t *testing.T) {
	type permissions struct {
		Network    bool
		Filesystem []string
		Devices    map[string]string
		Comment    string
	}

	printed := captureStdout(t, func() {
		PrintStructKeyVal(permissions{
			Network:    true,
			Filesystem: []string{"/home/user\x1b[1A\x1b[2K  - network: false"},
			Devices:    map[string]string{"dri\u009b2K": "yes\u009b1A"},
			Comment:    "harmless\u009b2K",
		})
	})

	if strings.ContainsRune(printed, 0x1b) {
		t.Fatalf("an escape byte reached the terminal: %q", printed)
	}
	if strings.ContainsRune(printed, 0x9b) {
		t.Fatalf("a single character control sequence reached the terminal: %q", printed)
	}
	if !strings.Contains(printed, `\x1b[1A`) || !strings.Contains(printed, `\u009b2K`) {
		t.Fatalf("the escaped form of the value is not what the reader was shown: %q", printed)
	}
	if !strings.Contains(printed, "network: true") {
		t.Fatalf("the permissions are no longer readable at all: %q", printed)
	}
}

// captureStdout answers with what was written to the output stream while the
// given work ran. The descriptor is replaced rather than the variable, because
// the logger took hold of os.Stdout when its package was initialised and would
// not notice a variable moving under it.
func captureStdout(t *testing.T, during func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("the output stream could not be captured: %v", err)
	}
	defer reader.Close()
	saved, err := unix.Dup(unix.Stdout)
	if err != nil {
		t.Skipf("the output stream cannot be duplicated here: %v", err)
	}
	if err := unix.Dup3(int(writer.Fd()), unix.Stdout, 0); err != nil {
		_ = unix.Close(saved)
		t.Skipf("the output stream cannot be redirected here: %v", err)
	}
	collected := make(chan string, 1)
	go func() {
		written, _ := io.ReadAll(reader)
		collected <- string(written)
	}()
	during()
	if err := unix.Dup3(saved, unix.Stdout, 0); err != nil {
		t.Fatalf("the output stream was not put back: %v", err)
	}
	_ = unix.Close(saved)
	// The reader sees the end of the pipe only once no descriptor is left open
	// on the other side, and the one above has just stopped being one.
	_ = writer.Close()
	return <-collected
}
