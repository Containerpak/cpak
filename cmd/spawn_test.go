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

func TestSetEnvironmentVariablesIdentifiesContainer(t *testing.T) {
	env := setEnvironmentVariables("container-id", "/rootfs", []string{"LANG=C"}, "/state", "/layers", "base|")
	want := []string{
		"LANG=C",
		"CPAK_CONTAINER_ID=container-id",
		"CPAK_ROOTFS=/rootfs",
		"CPAK_STATE_DIR=/state",
		"CPAK_LAYERS_DIR=/layers",
		"CPAK_LAYERS=base|",
	}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("environment: got %v, want %v", env, want)
	}
}
