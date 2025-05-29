/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-struct-flags/v1/binder"
	"github.com/spf13/cobra"
)

// NewOverrideCommand returns the cobra command for setting a single override key/value
func NewOverrideCommand() *cobra.Command {
	var key, value string
	cmd := &cobra.Command{
		Use:   "override APP_ORIGIN -k key -v value",
		Short: "Set override key/value for a cpak application",
		Long: `Set a single override key to a given value for an installed cpak application.
Use JSON field names for KEY (e.g. socketX11, fsExtra, env, etc.).
For list fields (fsExtra, env, allowedHostCommands), separate items with ':'`,
		Args: cobra.ExactArgs(1),
		RunE: RunOverride,
	}

	cmd.Flags().StringVarP(&key, "key", "k", "", "Override key (required)")
	cmd.Flags().StringVarP(&value, "value", "v", "", "Override value (required)")
	_ = cmd.MarkFlagRequired("key")
	_ = cmd.MarkFlagRequired("value")
	return cmd
}

// RunOverride sets the override key/value for a cpak application
func RunOverride(cmd *cobra.Command, args []string) error {
	appOrigin := strings.ToLower(args[0])

	key, err := cmd.Flags().GetString("key")
	if err != nil {
		return fmt.Errorf("a key is required")
	}
	value, err := cmd.Flags().GetString("value")
	if err != nil {
		return fmt.Errorf("a value is required")
	}

	// Initialize cpak and store
	cpk, err := cpak.NewCpak()
	if err != nil {
		return err
	}
	store, err := cpak.NewStore(cpk.Options.StorePath)
	if err != nil {
		return err
	}
	defer store.Close()

	apps, err := store.GetApplications()
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return fmt.Errorf("no cpak applications installed")
	}

	// Find the application by origin
	var sel types.Application
	for _, a := range apps {
		if a.Origin == appOrigin {
			sel = a
			break
		}
	}
	if sel.Origin == "" {
		return fmt.Errorf("application %q not found", appOrigin)
	}

	// Load existing override or fallback to manifest
	over := sel.ParsedOverride
	if userO, err := cpak.LoadOverride(appOrigin, sel.Version); err == nil {
		over = userO
	}

	// Initialize the flag binder
	binder, err := binder.NewBinder(&over, os.TempDir(), true)
	if err != nil {
		return err
	}

	argsList := []string{value}
	if key == "fsExtra" || key == "env" || key == "allowedHostCommands" {
		argsList = strings.Split(value, ":")
	}

	// Register the key with the binder
	if err := binder.Run(key, argsList); err != nil {
		return err
	}

	// Save the override
	if err := cpak.SaveOverride(over, appOrigin, sel.Version); err != nil {
		return err
	}

	fmt.Printf("Override %s=%s saved for %s\n", key, value, appOrigin)
	return nil
}
