/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package logger

import (
	"fmt"
	"os"

	clilog "github.com/mirkobrombin/go-cli-builder/v3/pkg/log"
)

var l clilog.Logger = newDefault()

// newDefault is the logger for the ordinary case, where cpak is the program
// the caller ran and its output is the output.
func newDefault() clilog.Logger {
	return clilog.NewWriter(os.Stdout, os.Stderr)
}

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

// ProxyMode sends everything cpak says to the error stream.
//
// A command that runs another program owns that program's standard output and
// nothing else may be written to it. cpak announcing what it is about to
// execute, or warning that Landlock is missing, lands in the middle of the
// output a caller is parsing: an SDK shim on PATH stops working inside `$( )`
// or a pipe, which is every use a toolchain has.
//
// It is a whole-stream switch and not a per-message decision, because the
// caller cannot tell which of cpak's lines are safe to receive and the answer
// is none of them.
func ProxyMode() {
	l = clilog.NewWriter(os.Stderr, os.Stderr)
}
