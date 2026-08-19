/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"strconv"
	"strings"
)

// Which arguments of a menu launch are files the user chose, and which are text
// the publisher wrote, used to be answered by two literal markers wrapped around
// the placeholder inside the exported Exec line.
//
// That put publisher text and cpak's own trust markers in one string, and the
// two parsers reading that string do not agree. The export filter dropped a
// token equal to a marker only after stripping quotes wrapping the whole token,
// while GLib, which is the parser the launcher really uses, unquotes an empty
// pair anywhere. So `""@@cpak:file-grant:start@@` passed the filter and arrived
// as a literal marker, opening a grant region the publisher controlled: every
// absolute path inside it was mounted read-only into the container, without
// being compared to any declared permission and without a line in the prompt.
//
// Nothing the publisher writes can say which arguments are files any more. cpak
// counts them when it writes the entry, because it is the one placing the
// placeholder, and carries the two counts in a flag of its own. The launcher
// substitutes the placeholder with however many files the user picked, so at
// launch the arguments are: `before` the publisher wrote, then the files, then
// `after` the publisher wrote. Two integers describe that exactly, whatever the
// text around them looks like.

// desktopFileSpan is how many publisher arguments sit on each side of the files.
type desktopFileSpan struct {
	Before int
	After  int
}

const desktopFileSpanFlag = "--desktop-file-span"

// desktopFilePlaceholders are the field codes a launcher replaces with what the
// user selected. The uppercase forms expand to any number of arguments, which is
// why a span is counted from both ends rather than as a position.
var desktopFilePlaceholders = map[string]bool{"%f": true, "%F": true, "%u": true, "%U": true}

func (s desktopFileSpan) String() string {
	return strconv.Itoa(s.Before) + "," + strconv.Itoa(s.After)
}

func parseDesktopFileSpan(value string) (desktopFileSpan, error) {
	before, after, found := strings.Cut(value, ",")
	if !found {
		return desktopFileSpan{}, fmt.Errorf("invalid desktop file span: %q", value)
	}
	leading, err := strconv.Atoi(before)
	if err != nil || leading < 0 {
		return desktopFileSpan{}, fmt.Errorf("invalid desktop file span: %q", value)
	}
	trailing, err := strconv.Atoi(after)
	if err != nil || trailing < 0 {
		return desktopFileSpan{}, fmt.Errorf("invalid desktop file span: %q", value)
	}
	return desktopFileSpan{Before: leading, After: trailing}, nil
}

// selects answers whether the argument at index is one the user chose, given how
// many arguments arrived in total.
//
// A launch that carries fewer arguments than the publisher wrote selected
// nothing: the placeholder expanded to nothing, which is what happens when an
// entry is started from the menu rather than by dropping a file on it.
func (s desktopFileSpan) selects(index, total int) bool {
	if total <= s.Before+s.After {
		return false
	}
	return index >= s.Before && index < total-s.After
}

// countDesktopFileSpan reads the publisher's argument list and answers how many
// arguments stand on each side of the one placeholder.
//
// Two placeholders are refused rather than described. A span is two numbers and
// cannot express two separate regions, and an entry that wants two is asking for
// something cpak has never granted.
func countDesktopFileSpan(arguments []string) (desktopFileSpan, bool, error) {
	placeholder := -1
	for index, argument := range arguments {
		if !desktopFilePlaceholders[strings.Trim(argument, `"`)] {
			continue
		}
		if placeholder >= 0 {
			return desktopFileSpan{}, false, fmt.Errorf("desktop entry names more than one file placeholder")
		}
		placeholder = index
	}
	if placeholder < 0 {
		return desktopFileSpan{}, false, nil
	}
	return desktopFileSpan{Before: placeholder, After: len(arguments) - placeholder - 1}, true, nil
}
