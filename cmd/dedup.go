package cmd

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
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

func dedupRun(cmd *cobra.Command, args []string) error {
	verbose, _ := cmd.Flags().GetBool("verbose")
	path, _ := cmd.Flags().GetString("path")

	if path == "" {
		return fmt.Errorf("path is mandatory")
	}

	if verbose {
		fmt.Printf("Deduplicating path %s\n", path)
	}

	c, err := cpak.NewCpak()
	if err != nil {
		return err
	}

	s, err := storage.NewStorage(c.Options.DaBaDeeStoreOptions)
	if err != nil {
		return err
	}

	h := hash.NewSHA256Generator()
	p := processor.NewDedupProcessor(path, s, h, 2)

	d := dabadee.NewDaBaDee(p, verbose)
	return d.Run()
}
