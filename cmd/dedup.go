/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package cmd

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/dabadee/pkg/dabadee"
	"github.com/mirkobrombin/dabadee/pkg/hash"
	"github.com/mirkobrombin/dabadee/pkg/processor"
	"github.com/mirkobrombin/dabadee/pkg/storage"
	"github.com/spf13/cobra"
)

func NewDedupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "dedup",
		Short:  "Deduplicate a path in the cpak dabadee store",
		RunE:   dedupRun,
		Hidden: true,
	}

	cmd.Flags().BoolP("verbose", "v", false, "enable verbose output")
	cmd.Flags().String("path", "", "the path to deduplicate")

	return cmd
}

func dedupRun(cmd *cobra.Command, args []string) (err error) {
	verbose, _ := cmd.Flags().GetBool("verbose")
	path, _ := cmd.Flags().GetString("path")

	logger.Printf("Deduplicating path %s in the DaBaDee storage..", path)

	if path == "" {
		err := fmt.Errorf("path is mandatory")
		logger.Error(err)
		return err
	}

	if verbose {
		logger.Printf("Deduplicating path %s", path)
	}

	c, err := cpak.NewCpak()
	if err != nil {
		return
	}

	s, err := storage.NewStorage(c.Options.DaBaDeeStoreOptions)
	if err != nil {
		return
	}

	h := hash.NewSHA256Generator()
	p := processor.NewDedupProcessor(path, "", s, h, 2)

	d := dabadee.NewDaBaDee(p, verbose)
	err = d.Run()
	if err != nil {
		return
	}

	logger.Printf("Deduplication completed successfully")
	return nil
}
