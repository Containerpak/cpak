/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package tools

import (
	"bufio"
	"os"
	"strings"

	"github.com/mirkobrombin/cpak/pkg/logger"
)

func ConfirmOperation(s string) bool {
	reader := bufio.NewReader(os.Stdin)
	logger.Printf("%s [y/N]: ", s)
	text, _ := reader.ReadString('\n')
	text = strings.Replace(text, "\n", "", -1)
	return strings.ToLower(text) == "y"
}
