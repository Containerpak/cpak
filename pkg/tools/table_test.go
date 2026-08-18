/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package tools

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/olekukonko/tablewriter"
	"golang.org/x/sys/unix"
)

// legacyTable is the renderer as it stood before the table became responsive.
// It is the specification for redirected output, which scripts parse.
func legacyTable(out *bytes.Buffer, header []string, data [][]string) {
	table := tablewriter.NewWriter(out)
	table.SetHeader(header)

	for _, v := range data {
		table.Append(v)
	}

	fmt.Fprintln(out)
	table.SetBorders(tablewriter.Border{Left: true, Top: false, Right: true, Bottom: false})
	table.SetCenterSeparator("|")
	table.Render()
	fmt.Fprintln(out)
}

func widestLine(t *testing.T, out string) int {
	t.Helper()
	widest := 0
	for _, line := range strings.Split(out, "\n") {
		if size := utf8.RuneCountInString(line); size > widest {
			widest = size
		}
	}
	return widest
}

func updateRows() ([]string, [][]string) {
	header := []string{"Name", "Origin", "Source", "Status", "From", "To", "Permissions", "Details"}
	data := [][]string{
		{"vscodium", "github.com/vscodium/vscodium-cpak", "branch", "updated", "main", "main", "", ""},
		{"libreoffice", "github.com/libreoffice/libreoffice-cpak", "branch", "failed", "main", "main", "", "could not resolve the manifest of the requested branch"},
	}
	return header, data
}

func TestWriteTableLeavesRedirectedOutputUnchanged(t *testing.T) {
	header, data := updateRows()

	var got bytes.Buffer
	writeTable(&got, header, data, 0)

	var want bytes.Buffer
	legacyTable(&want, header, data)

	if got.String() != want.String() {
		t.Fatalf("redirected output changed shape:\ngot:\n%s\nwant:\n%s", got.String(), want.String())
	}
}

func TestWriteTableFitsTheTerminalWidth(t *testing.T) {
	header, data := updateRows()

	for _, width := range []int{200, 120, 100, 80, 60, 50, 40, 30, 20} {
		var out bytes.Buffer
		writeTable(&out, header, data, width)
		if widest := widestLine(t, out.String()); widest > width {
			t.Fatalf("line of %d runes at terminal width %d:\n%s", widest, width, out.String())
		}
	}
}

func TestWriteTableWrapsNothingWhenTheWidthIsKnown(t *testing.T) {
	header := []string{"Name", "Details"}
	data := [][]string{{"vscodium", "could not resolve the manifest of the requested branch"}}

	var out bytes.Buffer
	writeTable(&out, header, data, 40)

	// One line per row, plus the two rules and the header, is what says the
	// row was cut instead of wrapped over several lines.
	rows := 0
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "vscodium") {
			rows++
		}
	}
	if rows != 1 {
		t.Fatalf("row rendered over %d lines instead of being truncated:\n%s", rows, out.String())
	}
	if !strings.Contains(out.String(), ellipsis) {
		t.Fatalf("truncated row carries no ellipsis:\n%s", out.String())
	}
	if strings.Contains(out.String(), "requested branch") {
		t.Fatalf("row was not truncated at all:\n%s", out.String())
	}
}

func TestWriteTableKeepsTheGridWhenItAlreadyFits(t *testing.T) {
	header := []string{"Name", "Status"}
	data := [][]string{{"vscodium", "updated"}}

	var out bytes.Buffer
	writeTable(&out, header, data, 120)

	if strings.Contains(out.String(), ellipsis) {
		t.Fatalf("a table that fits was truncated:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "vscodium") || !strings.Contains(out.String(), "updated") {
		t.Fatalf("a table that fits lost a value:\n%s", out.String())
	}
}

func TestWriteTableDropsColumnsNoRowFilledIn(t *testing.T) {
	header, data := updateRows()

	var out bytes.Buffer
	writeTable(&out, header, data, 120)

	if strings.Contains(out.String(), "PERMISSIONS") {
		t.Fatalf("column empty on every row was kept:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "DETAILS") {
		t.Fatalf("column filled in by one row was dropped:\n%s", out.String())
	}
}

func TestWriteTableKeepsEveryColumnWithoutRows(t *testing.T) {
	header := []string{"Name", "Status"}

	var out bytes.Buffer
	writeTable(&out, header, nil, 80)

	if !strings.Contains(out.String(), "NAME") || !strings.Contains(out.String(), "STATUS") {
		t.Fatalf("an empty table lost its header:\n%s", out.String())
	}
}

func TestWriteTableKeepsTheHeaderOfAnEmptyTableTooWideToFit(t *testing.T) {
	header, _ := updateRows()

	var out bytes.Buffer
	writeTable(&out, header, nil, 30)

	// There is no record to fall back to, so the header has to survive even
	// though it cannot be made to fit.
	if !strings.Contains(out.String(), "NAME") || !strings.Contains(out.String(), "DETAILS") {
		t.Fatalf("an empty table narrower than its header lost it:\n%s", out.String())
	}
}

func TestWriteTableFallsBackToRecordsWhenTheGridCannotFit(t *testing.T) {
	header, data := updateRows()

	var out bytes.Buffer
	writeTable(&out, header, data, 40)

	if strings.Contains(out.String(), "|") {
		t.Fatalf("a grid was drawn where none could fit:\n%s", out.String())
	}
	for _, name := range []string{"Name", "Origin", "Status"} {
		if !strings.Contains(out.String(), name) {
			t.Fatalf("record block lost the %s label:\n%s", name, out.String())
		}
	}
	if !strings.Contains(out.String(), "vscodium") || !strings.Contains(out.String(), "libreoffice") {
		t.Fatalf("record block lost a record:\n%s", out.String())
	}
	if widest := widestLine(t, out.String()); widest > 40 {
		t.Fatalf("record block line of %d runes at terminal width 40:\n%s", widest, out.String())
	}
}

func TestWriteTableSeparatesTheRecordsOfTheFallback(t *testing.T) {
	header, data := updateRows()

	var out bytes.Buffer
	writeTable(&out, header, data, 40)

	blocks := strings.Split(strings.TrimSpace(out.String()), "\n\n")
	if len(blocks) != len(data) {
		t.Fatalf("got %d blocks for %d records:\n%s", len(blocks), len(data), out.String())
	}
}

func TestFittedWidthsGivesUpAtTheColumnFloor(t *testing.T) {
	header := []string{"One", "Two", "Three", "Four", "Five", "Six"}
	data := [][]string{{"a", "b", "c", "d", "e", "f"}}

	if widths := fittedWidths(header, data, 30); widths != nil {
		t.Fatalf("six columns claimed to fit 30 columns: %v", widths)
	}
	widths := fittedWidths(header, data, 120)
	if widths == nil {
		t.Fatal("six short columns did not fit 120 columns")
	}
	for column, width := range widths {
		if width < 1 {
			t.Fatalf("column %d fitted to width %d", column, width)
		}
	}
}

func TestGridWidthMatchesWhatIsDrawn(t *testing.T) {
	header := []string{"Name", "Status"}
	data := [][]string{{"vscodium", "updated"}}

	widths := naturalWidths(header, data)
	var out bytes.Buffer
	renderGrid(&out, header, data, false)

	if widest := widestLine(t, out.String()); widest != gridWidth(widths) {
		t.Fatalf("grid drawn %d wide, gridWidth says %d:\n%s", widest, gridWidth(widths), out.String())
	}
}

func TestFitCutsOnTheDisplayWidth(t *testing.T) {
	cases := []struct {
		value string
		width int
		want  string
	}{
		{"vscodium", 8, "vscodium"},
		{"vscodium", 7, "vsco..."},
		{"vscodium", 3, "..."},
		{"vscodium", 2, ".."},
		{"vscodium", 0, ""},
		{"a\nb\tc", 8, "a b c"},
		{"  padded  ", 8, "padded"},
	}
	for _, c := range cases {
		if got := fit(c.value, c.width); got != c.want {
			t.Fatalf("fit(%q, %d) = %q, want %q", c.value, c.width, got, c.want)
		}
	}
}

// openTerminal returns the two ends of a pseudo terminal of the given width,
// so that the width detection is measured on a real terminal.
func openTerminal(t *testing.T, width int) (master, slave *os.File) {
	t.Helper()
	control, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pseudo terminal available: %v", err)
	}

	if err := unix.IoctlSetPointerInt(int(control.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		control.Close()
		t.Fatalf("unlock the pseudo terminal: %v", err)
	}
	number, err := unix.IoctlGetInt(int(control.Fd()), unix.TIOCGPTN)
	if err != nil {
		control.Close()
		t.Fatalf("name the pseudo terminal: %v", err)
	}
	terminal, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		control.Close()
		t.Fatalf("open the pseudo terminal: %v", err)
	}
	size := unix.Winsize{Row: 40, Col: uint16(width)}
	if err := unix.IoctlSetWinsize(int(terminal.Fd()), unix.TIOCSWINSZ, &size); err != nil {
		control.Close()
		terminal.Close()
		t.Fatalf("size the pseudo terminal: %v", err)
	}
	return control, terminal
}

// captureShowTable calls the exported entry point with stdout pointed at the
// given file and returns everything that came out of the other end.
func captureShowTable(t *testing.T, reader, writer *os.File, header []string, data [][]string) string {
	t.Helper()
	captured := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		io.Copy(&out, reader)
		captured <- out.String()
	}()

	stdout := os.Stdout
	os.Stdout = writer
	ShowTable(header, data)
	os.Stdout = stdout
	writer.Close()

	return <-captured
}

func TestShowTableFitsARealTerminal(t *testing.T) {
	master, slave := openTerminal(t, 80)
	defer master.Close()

	header, data := updateRows()
	out := captureShowTable(t, master, slave, header, data)

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if size := utf8.RuneCountInString(line); size > 80 {
			t.Fatalf("line of %d runes on an 80 column terminal: %q", size, line)
		}
	}
	if !strings.Contains(out, ellipsis) {
		t.Fatalf("a table too wide for 80 columns was not truncated:\n%s", out)
	}
	if strings.Contains(out, "PERMISSIONS") {
		t.Fatalf("column empty on every row survived on a terminal:\n%s", out)
	}
}

func TestShowTableLeavesAPipeUntouched(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	header, data := updateRows()
	got := captureShowTable(t, reader, writer, header, data)

	var want bytes.Buffer
	legacyTable(&want, header, data)

	if got != want.String() {
		t.Fatalf("redirected output changed shape:\ngot:\n%s\nwant:\n%s", got, want.String())
	}
}

func TestTerminalWidthIsZeroWhenNothingIsWatching(t *testing.T) {
	if width := terminalWidth(&bytes.Buffer{}); width != 0 {
		t.Fatalf("a buffer reported a terminal width of %d", width)
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	if width := terminalWidth(writer); width != 0 {
		t.Fatalf("a pipe reported a terminal width of %d", width)
	}
}
