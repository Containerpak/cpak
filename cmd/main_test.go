/*
 * Copyright (c) 2026 Fabricators
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if argsFile := os.Getenv("CPAK_NESTED_TEST_ARGS_FILE"); argsFile != "" {
		content := strings.Join(os.Args[1:], "\n") + "\n"
		if err := os.WriteFile(argsFile, []byte(content), 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		fmt.Println("nested-output")
		status, err := strconv.Atoi(os.Getenv("CPAK_NESTED_TEST_STATUS"))
		if err != nil {
			os.Exit(125)
		}
		os.Exit(status)
	}
	os.Exit(m.Run())
}
