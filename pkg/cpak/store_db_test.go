/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cpak

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/types"
	"github.com/mirkobrombin/go-slipstream/pkg/wal"
)

func TestStoreFindsApplicationByOriginAfterReopen(t *testing.T) {
	path := t.TempDir()
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := types.Application{CpakId: "application-id", Origin: "github.com/containerpak/example", Version: "main"}
	if err = store.NewApplication(want); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.GetApplicationByOrigin(want.Origin, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.CpakId != want.CpakId {
		t.Fatalf("application: got %q, want %q", got.CpakId, want.CpakId)
	}
}

func TestOpenWALWaitsForAnotherProcess(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "apps")
	first, err := wal.NewManager(directory)
	if err != nil {
		t.Fatal(err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = first.Close()
		close(released)
	}()

	second, err := openWAL(directory, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	<-released
}

func TestOpenWALStopsWaitingAtTheDeadline(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "apps")
	first, err := wal.NewManager(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	started := time.Now()
	_, err = openWAL(directory, 40*time.Millisecond)
	if !errors.Is(err, wal.ErrDirectoryLocked) {
		t.Fatalf("error: got %v, want %v", err, wal.ErrDirectoryLocked)
	}
	if time.Since(started) < 40*time.Millisecond {
		t.Fatal("lock wait returned before its deadline")
	}
}
