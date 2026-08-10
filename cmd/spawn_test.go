/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cmd

import (
	"reflect"
	"testing"
)

func TestLayerDirectoriesUseOverlayPriority(t *testing.T) {
	got := layerDirectories("/layers", []string{"base", "top", "runtime"})
	want := []string{"/layers/runtime", "/layers/top", "/layers/base"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected overlay order: got %v, want %v", got, want)
	}
}
