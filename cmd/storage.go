package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
)

type StorageCmd struct {
	Action string `arg:"action" help:"Action: status, migrate or verify"`
	Repair bool   `cli:"repair" help:"Repair failed verification checks"`
	JSON   bool   `cli:"json,j" help:"Print output as JSON"`

	cli.Base
}

func (c *StorageCmd) Run() error {
	cp, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	switch strings.ToLower(c.Action) {
	case "status":
		status, err := cp.StorageStatus()
		if err != nil {
			return err
		}
		if c.JSON {
			return printStorageJSON(status)
		}
		c.Logger.Info("Storage driver: %s", status.Driver)
		c.Logger.Info("Prepared layers: %d of %d", status.Prepared, status.Layers)
		return nil
	case "migrate":
		// Older updaters already invoke this step in the replacement binary.
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve cpak executable: %w", err)
		}
		if err := prepareUpdatedRuntime(context.Background(), executable); err != nil {
			return err
		}
		status, err := cp.PrepareInstalledStorage()
		if err != nil {
			return err
		}
		if c.JSON {
			return printStorageJSON(status)
		}
		c.Logger.Success("Prepared %d storage layers with %s", status.Prepared, status.Driver)
		return nil
	case "verify":
		result, err := cp.VerifyPreparedStorage(c.Repair)
		if err != nil {
			return err
		}
		if c.JSON {
			return printStorageJSON(result)
		}
		c.Logger.Success("Verified %d storage layers", result.Verified)
		return nil
	default:
		return fmt.Errorf("unsupported storage action %q", c.Action)
	}
}

func printStorageJSON(value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
