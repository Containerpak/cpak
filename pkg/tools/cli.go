/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
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
