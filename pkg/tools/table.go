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

	"github.com/olekukonko/tablewriter"
	"golang.org/x/term"
)

// ellipsis marks a value the terminal was too narrow to show whole.
const ellipsis = "..."

// minColumnWidth is the narrowest a column may become before the grid stops
// saying anything: below it every cell is an ellipsis and a block per record
// carries more than the grid does.
const minColumnWidth = 8

// ShowTable prints a table to stdout, fitted to the terminal when stdout is
// one. Redirected output is left exactly as it was, because scripts parse it.
func ShowTable(header []string, data [][]string) {
	writeTable(os.Stdout, header, data, terminalWidth(os.Stdout))
}

// terminalWidth reports the usable width of out, and 0 when out is not a
// terminal, which is the signal to format for a parser instead of a reader.
func terminalWidth(out io.Writer) int {
	file, ok := out.(*os.File)
	if !ok {
		return 0
	}
	descriptor := int(file.Fd())
	if !term.IsTerminal(descriptor) {
		return 0
	}
	width, _, err := term.GetSize(descriptor)
	if err != nil || width <= 0 {
		return 0
	}
	return width
}

func writeTable(out io.Writer, header []string, data [][]string, width int) {
	if width <= 0 {
		renderGrid(out, header, data, true)
		return
	}

	header, data = dropEmptyColumns(header, data)
	widths := fittedWidths(header, data, width)
	if widths == nil {
		// A table with no rows has no record to fall back to, and its header
		// is all there is left to show.
		if len(data) == 0 {
			renderGrid(out, header, data, false)
			return
		}
		renderRecords(out, header, data, width)
		return
	}
	renderGrid(out, fitRow(header, widths), fitRows(data, widths), false)
}

// renderGrid draws the table. Wrapping is left on only for the redirected
// output, where the width is unknown and the previous shape has to be kept.
func renderGrid(out io.Writer, header []string, data [][]string, wrap bool) {
	table := tablewriter.NewWriter(out)
	// Both the header and the rows are measured as they are added, so the
	// wrapping has to be decided before either of them goes in.
	table.SetAutoWrapText(wrap)
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

// renderRecords prints one block per record, the shape left when not even a
// truncated grid fits the terminal.
func renderRecords(out io.Writer, header []string, data [][]string, width int) {
	label := 0
	for _, name := range header {
		if size := tablewriter.DisplayWidth(name); size > label {
			label = size
		}
	}
	// The label never takes more than half of a narrow terminal, otherwise the
	// values it names have nowhere left to go.
	if label > width/2 {
		label = width / 2
	}

	fmt.Fprintln(out)
	for index, row := range data {
		if index > 0 {
			fmt.Fprintln(out)
		}
		for column, name := range header {
			value := ""
			if column < len(row) {
				value = row[column]
			}
			line := fmt.Sprintf("%-*s  %s", label, fit(name, label), fit(value, width-label-2))
			fmt.Fprintln(out, strings.TrimRight(line, " "))
		}
	}
	fmt.Fprintln(out)
}

// dropEmptyColumns removes the columns no row has a value for, which is how a
// table that carries a field nobody filled in stops spending width on it.
func dropEmptyColumns(header []string, data [][]string) ([]string, [][]string) {
	if len(data) == 0 {
		return header, data
	}

	keep := make([]int, 0, len(header))
	for column := range header {
		if columnHasValue(data, column) {
			keep = append(keep, column)
		}
	}
	if len(keep) == 0 || len(keep) == len(header) {
		return header, data
	}

	kept := make([]string, 0, len(keep))
	for _, column := range keep {
		kept = append(kept, header[column])
	}
	rows := make([][]string, 0, len(data))
	for _, row := range data {
		cells := make([]string, 0, len(keep))
		for _, column := range keep {
			if column < len(row) {
				cells = append(cells, row[column])
				continue
			}
			cells = append(cells, "")
		}
		rows = append(rows, cells)
	}
	return kept, rows
}

func columnHasValue(data [][]string, column int) bool {
	for _, row := range data {
		if column < len(row) && strings.TrimSpace(row[column]) != "" {
			return true
		}
	}
	return false
}

// fittedWidths returns the column widths a grid of the given terminal width can
// show, or nil when the grid cannot be made to fit at all.
func fittedWidths(header []string, data [][]string, width int) []int {
	widths := naturalWidths(header, data)
	for gridWidth(widths) > width {
		widest := widestColumn(widths)
		if widest < 0 {
			return nil
		}
		widths[widest]--
	}
	return widths
}

func naturalWidths(header []string, data [][]string) []int {
	widths := make([]int, len(header))
	for column, name := range header {
		widths[column] = tablewriter.DisplayWidth(name)
	}
	for _, row := range data {
		for column, value := range row {
			if column >= len(widths) {
				break
			}
			if size := tablewriter.DisplayWidth(oneLine(value)); size > widths[column] {
				widths[column] = size
			}
		}
	}
	return widths
}

// gridWidth is the width tablewriter draws: a separator before every column,
// a space on each side of every cell, and a separator closing the row.
func gridWidth(widths []int) int {
	total := 1
	for _, width := range widths {
		total += width + 3
	}
	return total
}

// widestColumn returns the column to take a character from, or -1 when every
// column is already at the floor.
func widestColumn(widths []int) int {
	widest := -1
	for column, width := range widths {
		if width <= minColumnWidth {
			continue
		}
		if widest < 0 || width > widths[widest] {
			widest = column
		}
	}
	return widest
}

func fitRows(data [][]string, widths []int) [][]string {
	rows := make([][]string, 0, len(data))
	for _, row := range data {
		rows = append(rows, fitRow(row, widths))
	}
	return rows
}

func fitRow(row []string, widths []int) []string {
	cells := make([]string, 0, len(row))
	for column, value := range row {
		// A cell no header names cannot be given a width, and printing it
		// anyway is what would push the row past the terminal.
		if column >= len(widths) {
			break
		}
		cells = append(cells, fit(value, widths[column]))
	}
	return cells
}

// fit puts a value on a single line and cuts it with an ellipsis, because a
// cell that wraps is what turns a wide table into a wall.
func fit(value string, width int) string {
	value = oneLine(value)
	if width <= 0 {
		return ""
	}
	if tablewriter.DisplayWidth(value) <= width {
		return value
	}
	if width <= len(ellipsis) {
		return ellipsis[:width]
	}

	limit := width - len(ellipsis)
	kept := 0
	var cut strings.Builder
	for _, character := range value {
		size := tablewriter.DisplayWidth(string(character))
		if kept+size > limit {
			break
		}
		cut.WriteRune(character)
		kept += size
	}
	return cut.String() + ellipsis
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
