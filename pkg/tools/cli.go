/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
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
