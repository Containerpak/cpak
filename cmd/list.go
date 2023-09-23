package cmd

/*
cpak list
*/

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/tools"
	"github.com/spf13/cobra"
)

func NewListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all installed packages",
		Args:  cobra.NoArgs,
		RunE:  ListPackages,
	}

	cmd.Flags().BoolP("json", "j", false, "Print output in JSON format")

	return cmd
}

func listError(iErr error) (err error) {
	err = fmt.Errorf("an error occurred while listing cpak(s): %s", iErr)
	return
}

func ListPackages(cmd *cobra.Command, args []string) error {
	jsonFlag, err := cmd.Flags().GetBool("json")
	if err != nil {
		return listError(err)
	}

	cpak, err := cpak.NewCpak()
	if err != nil {
		return listError(err)
	}

	apps, err := cpak.GetInstalledApps()
	if err != nil {
		return listError(err)
	}

	if !jsonFlag {
		header := []string{"Name", "Version", "Timestamp", "Origin", "Remote"}
		data := [][]string{}
		for _, app := range apps {
			remote := ""
			switch {
			case app.Branch != "":
				remote = app.Branch
			case app.Release != "":
				remote = app.Release
			case app.Commit != "":
				remote = app.Commit
			default:
				remote = "unknown"
			}
			data = append(data, []string{app.Name, app.Version, app.Timestamp.Format(time.RFC3339), app.Origin, remote})
		}
		tools.ShowTable(header, data)
	} else {
		jsonBytes, err := json.MarshalIndent(apps, "", "  ")
		if err != nil {
			return listError(err)
		}

		fmt.Println(string(jsonBytes))
	}

	return nil
}
