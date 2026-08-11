/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"testing"

	"github.com/mirkobrombin/cpak/pkg/types"
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
