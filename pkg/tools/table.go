/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package tools

import (
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
)

// ShowTable prints a table to stdout.
func ShowTable(header []string, data [][]string) {
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader(header)

	for _, v := range data {
		table.Append(v)
	}

	fmt.Println()
	table.SetBorders(tablewriter.Border{Left: true, Top: false, Right: true, Bottom: false})
	table.SetCenterSeparator("|")
	table.Render()
	fmt.Println()
}
