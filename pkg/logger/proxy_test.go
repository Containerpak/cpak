/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package logger

import (
	"io"
	"os"
	"strings"
	"testing"
)

// An SDK shim runs a toolchain inside a package and hands its output back, so
// a caller writes version=$(zig version) and reads whatever came out. Every
// line cpak adds to that stream is read as the tool's answer, which is why the
// shims were unusable in a pipeline until this existed.
func TestProxyModeLeavesTheOutputStreamToTheProgram(t *testing.T) {
	saved := l
	t.Cleanup(func() { l = saved })

	out, errOut := captureStandardStreams(t, func() {
		ProxyMode()
		Info("executing something on behalf of a caller")
		Warn("landlock is unavailable")
		Success("done")
	})

	if out != "" {
		t.Fatalf("cpak wrote to the program's output stream: %q", out)
	}
	for _, expected := range []string{"executing", "landlock", "done"} {
		if !strings.Contains(errOut, expected) {
			t.Fatalf("%q was not reported anywhere: %q", expected, errOut)
		}
	}
}

// Without proxy mode cpak is the program, and its output belongs on the output
// stream. The switch has to be a switch, not a permanent silence.
func TestTheOrdinaryLoggerStillSpeaksOnTheOutputStream(t *testing.T) {
	saved := l
	t.Cleanup(func() { l = saved })

	out, _ := captureStandardStreams(t, func() {
		l = newDefault()
		Info("this is cpak talking about itself")
	})
	if !strings.Contains(out, "cpak talking") {
		t.Fatalf("the ordinary logger says nothing on the output stream: %q", out)
	}
}

// captureStandardStreams runs a function with both process streams replaced,
// which is the only way to see where a logger built from os.Stdout and
// os.Stderr actually aims.
func captureStandardStreams(t *testing.T, run func()) (string, string) {
	t.Helper()
	outRead, outWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outWrite, errWrite

	run()

	os.Stdout, os.Stderr = savedOut, savedErr
	if err := outWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := errWrite.Close(); err != nil {
		t.Fatal(err)
	}
	outBytes, err := io.ReadAll(outRead)
	if err != nil {
		t.Fatal(err)
	}
	errBytes, err := io.ReadAll(errRead)
	if err != nil {
		t.Fatal(err)
	}
	return string(outBytes), string(errBytes)
}
