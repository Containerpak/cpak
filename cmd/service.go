package cmd

/*
cpak start-service
*/

import (
	"fmt"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/spf13/cobra"
)

func NewServiceCommand() *cobra.Command {

	// we have to accept also unhandled flags which will be passed to the binary
	cmd := &cobra.Command{
		Use:  "start-service",
		RunE: RunService,
	}
	return cmd
}

func runSError(iErr error) (err error) {
	err = fmt.Errorf("an error occurred while starting the cpak service: %s", iErr)
	return
}

func RunService(cmd *cobra.Command, args []string) (err error) {
	cpak, err := cpak.NewCpak()
	if err != nil {
		fmt.Println("cpak service exited with error!", err)
		return runSError(err)
	}

	err = cpak.StartSocketListener()
	if err != nil {
		fmt.Println("cpak service exited with error!", err)
		return runSError(err)
	}

	fmt.Println("cpak service exited successfully!")
	return nil
}
