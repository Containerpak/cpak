/*
 * Copyright (c) 2026 Fabricators
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"context"
	"errors"
	"strings"
	"testing"

	fvsrepo "github.com/fvs-lab/fvs2/repo"
)

func TestFVSExportRefusesOversizedFilesBeforeReadingThem(t *testing.T) {
	entries := map[string]fvsViewEntry{
		"usr/share/icons/demo.svg": {
			file: fvsrepo.FileEntry{
				Path: "usr/share/icons/demo.svg",
				Kind: string(fvsrepo.EntryFile),
				Size: iconSizeLimit + 1,
			},
		},
	}
	_, err := fvsViewFileData(context.Background(), entries, "usr/share/icons/demo.svg", iconSizeLimit)
	if err == nil || !strings.Contains(err.Error(), "export limit") {
		t.Fatalf("oversized icon returned %v", err)
	}
}

func TestFVSExportKeepsLinksInsideTheImageView(t *testing.T) {
	entries := map[string]fvsViewEntry{
		"usr/share/icons/demo.svg": {
			file: fvsrepo.FileEntry{
				Path: "usr/share/icons/demo.svg",
				Kind: string(fvsrepo.EntrySymlink),
				Link: "/dev/zero",
			},
		},
	}
	_, err := fvsViewFileData(context.Background(), entries, "usr/share/icons/demo.svg", iconSizeLimit)
	if err == nil {
		t.Fatal("an image symlink escaped to a host path")
	}
}

func TestBoundedExportBufferNeverGrowsPastItsLimit(t *testing.T) {
	buffer := boundedExportBuffer{limit: 4}
	if _, err := buffer.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("5")); !errors.Is(err, errFVSExportLimit) {
		t.Fatalf("write past the limit returned %v", err)
	}
	if buffer.Len() != 4 {
		t.Fatalf("buffer grew to %d bytes", buffer.Len())
	}
}
