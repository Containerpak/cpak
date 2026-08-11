package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type MigrateManifestCmd struct {
	Manifest string `arg:"manifest" help:"Path to the v1 manifest"`
	Output   string `cli:"output,o" help:"Path for the v2 manifest"`

	cli.Base
}

func (c *MigrateManifestCmd) Run() error {
	if c.Output == "" {
		return fmt.Errorf("output is mandatory")
	}
	data, err := os.ReadFile(c.Manifest)
	if err != nil {
		return err
	}
	manifest, err := cpak.DecodeManifest(data)
	if err != nil {
		return err
	}
	if err := cpak.MigrateManifest(manifest); err != nil {
		return err
	}
	data, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.Output, data, 0644); err != nil {
		return err
	}
	c.Logger.Success("Created migrated manifest at %s", c.Output)
	return nil
}
