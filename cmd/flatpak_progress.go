/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/logger"
	"github.com/mirkobrombin/cpak/pkg/ostree"
	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-cli-builder/v3/pkg/cli"
	clilog "github.com/mirkobrombin/go-cli-builder/v3/pkg/log"
)

// Exit codes a program driving cpak can tell apart. Anything else exits 1.
const (
	exitPermissionsRefused = 10
	exitUnavailable        = 11
	exitVerification       = 12
)

var errPermissionsRefused = errors.New("the permissions were not approved")

// exitCode reads why an operation failed. A forged answer is reported as
// such even when the connection also failed along the way, since it is the
// one a user must not retry past.
func exitCode(err error) int {
	var network net.Error
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ostree.ErrVerification):
		return exitVerification
	case errors.Is(err, ostree.ErrUnavailable), errors.As(err, &network):
		return exitUnavailable
	case errors.Is(err, errPermissionsRefused):
		return exitPermissionsRefused
	}
	return 1
}

// updateExitCode answers for the first application that did not update for
// a reason the exit code can tell, and 0 when none did.
func updateExitCode(results []types.UpdateResult) int {
	for _, result := range results {
		switch {
		case result.Status == types.UpdateStatusPermissionDenied:
			return exitPermissionsRefused
		case result.Status == types.UpdateStatusFailed && exitCode(result.Err) > 1:
			return exitCode(result.Err)
		}
	}
	return 0
}

type progressEvent struct {
	cpak.FlatpakProgress
	Error   string               `json:"error,omitempty"`
	Code    *int                 `json:"code,omitempty"`
	Results []types.UpdateResult `json:"results,omitempty"`
}

// progressWriter prints one JSON object per line. The last one has the phase
// done, carries the exit code and, when the operation failed, the error, so
// the program reading it never has to parse what is written for people.
type progressWriter struct {
	mu     sync.Mutex
	output io.Writer
}

// newProgressWriter takes standard output for the events, and sends what
// cpak writes for people to the error stream.
func newProgressWriter(base *cli.Base) *progressWriter {
	logger.MachineOutputMode()
	base.Logger = clilog.NewWriter(os.Stderr, os.Stderr)
	return &progressWriter{output: os.Stdout}
}

func (w *progressWriter) write(event progressEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintln(w.output, string(data))
}

func (w *progressWriter) report(progress cpak.FlatpakProgress) {
	w.write(progressEvent{FlatpakProgress: progress})
}

func (w *progressWriter) done(code int, err error, results []types.UpdateResult) {
	event := progressEvent{FlatpakProgress: cpak.FlatpakProgress{Phase: "done"}, Code: &code, Results: results}
	if err != nil {
		event.Error = err.Error()
	}
	w.write(event)
}

// exitWith ends a command with the exit code err stands for. The error is
// printed here, because main does not print one that only carries a code.
func exitWith(err error) error {
	code := exitCode(err)
	if code <= 1 {
		return err
	}
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	return &types.ExitError{Code: code}
}
