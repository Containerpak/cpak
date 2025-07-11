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
	"github.com/spf13/cobra"
)

func NewServiceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "start-service",
		RunE:   RunService,
		Hidden: true,
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
		logger.Println("cpak service exited with error!", err)
		return runSError(err)
	}

	err = cpak.StartSocketListener()
	if err != nil {
		logger.Println("cpak service exited with error!", err)
		return runSError(err)
	}

	logger.Println("cpak service exited successfully!")
	return nil
}
