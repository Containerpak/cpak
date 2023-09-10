package tools

import (
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
)

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
