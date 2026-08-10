/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package cpak

import (
	"reflect"
	"testing"
)

func TestGetNvidiaLibraryDirs(t *testing.T) {
	files := []string{
		"/usr/lib64/libnvidia-glsi.so.565.77",
		"/usr/lib64/libcuda.so.1",
		"/usr/lib/libnvidia-eglcore.so.565.77",
		"/usr/share/vulkan/icd.d/nvidia_icd.json",
		"/usr/bin/nvidia-smi",
	}

	want := []string{"/usr/lib", "/usr/lib64"}
	if got := GetNvidiaLibraryDirs(files); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetNvidiaLibraryDirs() = %v, want %v", got, want)
	}
}
