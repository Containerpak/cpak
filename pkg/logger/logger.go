/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package logger

import (
	"fmt"

	clilog "github.com/mirkobrombin/go-cli-builder/v3/pkg/log"
)

var l clilog.Logger = clilog.New()

func Info(args ...interface{}) {
	l.Info(fmt.Sprint(args...))
}

func Infof(format string, args ...interface{}) {
	l.Info(format, args...)
}

func Success(args ...interface{}) {
	l.Success(fmt.Sprint(args...))
}

func Successf(format string, args ...interface{}) {
	l.Success(format, args...)
}

func Warn(args ...interface{}) {
	l.Warning(fmt.Sprint(args...))
}

func Warnf(format string, args ...interface{}) {
	l.Warning(format, args...)
}

func Error(args ...interface{}) {
	l.Error(fmt.Sprint(args...))
}

func Errorf(format string, args ...interface{}) {
	l.Error(format, args...)
}

func Println(args ...interface{}) {
	l.Info(fmt.Sprint(args...))
}

func Printf(format string, args ...interface{}) {
	l.Info(format, args...)
}
