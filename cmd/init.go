package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/spf13/cobra"
)

// NewInitCommand creates the `init` command for scaffolding a cpak manifest.
func NewInitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a new cpak manifest in the current directory",
		RunE:  initRun,
	}

	// flags required...
	cmd.Flags().StringP("name", "n", "", "Name of the application (required)")
	cmd.MarkFlagRequired("name")

	cmd.Flags().StringP("version", "v", "", "Version of the application, e.g. v1.0.0 (required)")
	cmd.MarkFlagRequired("version")

	cmd.Flags().StringP("description", "d", "", "Short description of the application (required)")
	cmd.MarkFlagRequired("description")

	cmd.Flags().StringP("image", "i", "", "OCI image reference (required)")
	cmd.MarkFlagRequired("image")
	cmd.Flags().StringSliceP("binary", "b", []string{}, "Path to a binary to expose (can be repeated, must be absolute paths, required)")

	// Optional manifest fields
	cmd.Flags().StringSliceP("desktop-entry", "e", []string{}, "Path to a desktop entry file (can be repeated)")
	cmd.Flags().StringSliceP("dependency", "D", []string{}, "Origin of a cpak dependency (can be repeated)")
	cmd.Flags().StringSliceP("addon", "a", []string{}, "Name of an addon (can be repeated)")
	cmd.Flags().IntP("idle-time", "I", 0, "Idle time in minutes after which to destroy the container")

	return cmd
}

// initRun executes the scaffolding of cpak.json based on provided flags.
func initRun(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	version, _ := cmd.Flags().GetString("version")
	desc, _ := cmd.Flags().GetString("description")
	image, _ := cmd.Flags().GetString("image")
	binaries, _ := cmd.Flags().GetStringSlice("binary")
	desktops, _ := cmd.Flags().GetStringSlice("desktop-entry")
	deps, _ := cmd.Flags().GetStringSlice("dependency")
	addons, _ := cmd.Flags().GetStringSlice("addon")
	idle, _ := cmd.Flags().GetInt("idle-time")

	manifest := types.CpakManifest{
		Name:           name,
		Description:    desc,
		Version:        version,
		Image:          image,
		Binaries:       binaries,
		DesktopEntries: desktops,
		Dependencies:   []types.Dependency{},
		Addons:         addons,
		IdleTime:       idle,
		Override:       types.NewOverride(),
	}
	for _, origin := range deps {
		manifest.Dependencies = append(manifest.Dependencies, types.Dependency{Origin: origin})
	}

	if err := cpak.ValidateManifest(&manifest); err != nil {
		return fmt.Errorf("cpak.json is invalid:\n%s", err)
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize manifest: %w", err)
	}
	if err := os.WriteFile("cpak.json", data, 0644); err != nil {
		return fmt.Errorf("failed to write cpak.json: %w", err)
	}

	fmt.Println("Created cpak.json successfully.")
	return nil
}
