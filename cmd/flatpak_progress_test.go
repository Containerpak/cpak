/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/mirkobrombin/cpak/pkg/cpak"
	"github.com/mirkobrombin/cpak/pkg/ostree"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestExitCodesTellFailuresApart(t *testing.T) {
	forged := fmt.Errorf("import app/org.example.App/x86_64/stable: %w", errors.Join(errors.New("object does not match"), ostree.ErrVerification))
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("the store is locked"), 1},
		{fmt.Errorf("install: %w", errPermissionsRefused), exitPermissionsRefused},
		{fmt.Errorf("fetch: %w", errors.Join(errors.New("503"), ostree.ErrUnavailable)), exitUnavailable},
		{fmt.Errorf("fetch: %w", &net.OpError{Op: "dial", Err: errors.New("refused")}), exitUnavailable},
		{forged, exitVerification},
		// A forged answer that also broke the connection is still forged.
		{errors.Join(forged, ostree.ErrUnavailable), exitVerification},
	}
	for _, testCase := range cases {
		if code := exitCode(testCase.err); code != testCase.want {
			t.Fatalf("%v exits %d, want %d", testCase.err, code, testCase.want)
		}
	}
}

func TestUpdateExitCodeAnswersForTheFirstTellableFailure(t *testing.T) {
	results := []types.UpdateResult{
		{Status: types.UpdateStatusUpdated},
		{Status: types.UpdateStatusFailed, Err: errors.New("disk full")},
		{Status: types.UpdateStatusFailed, Err: errors.Join(errors.New("bad"), ostree.ErrVerification)},
	}
	if code := updateExitCode(results); code != exitVerification {
		t.Fatalf("exit code %d", code)
	}
	if code := updateExitCode([]types.UpdateResult{{Status: types.UpdateStatusPermissionDenied}}); code != exitPermissionsRefused {
		t.Fatalf("a refused permission exits %d", code)
	}
	if code := updateExitCode([]types.UpdateResult{{Status: types.UpdateStatusUpToDate}}); code != 0 {
		t.Fatalf("an update with nothing to tell exits %d", code)
	}
}

func TestProgressEndsWithTheExitCodeAndTheError(t *testing.T) {
	var output bytes.Buffer
	progress := &progressWriter{output: &output}
	progress.report(cpak.FlatpakProgress{Phase: cpak.FlatpakPhaseDownload, Ref: "app/org.example.App/x86_64/stable", ObjectsDone: 3, ObjectsTotal: 10, BytesDone: 4096})
	progress.done(exitUnavailable, errors.New("the remote did not answer"), nil)

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %q", output.String())
	}
	var step map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &step); err != nil {
		t.Fatal(err)
	}
	if step["phase"] != "download" || step["objects_done"] != 3.0 || step["objects_total"] != 10.0 || step["bytes_done"] != 4096.0 {
		t.Fatalf("the download step reads %v", step)
	}
	if _, ok := step["code"]; ok {
		t.Fatal("a step before the end carries an exit code")
	}
	var last map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &last); err != nil {
		t.Fatal(err)
	}
	if last["phase"] != "done" || last["code"] != float64(exitUnavailable) || last["error"] != "the remote did not answer" {
		t.Fatalf("the last event reads %v", last)
	}

	output.Reset()
	progress.done(0, nil, nil)
	if !strings.Contains(output.String(), `"code":0`) || strings.Contains(output.String(), `"error"`) {
		t.Fatalf("a success ends with %s", output.String())
	}
}
