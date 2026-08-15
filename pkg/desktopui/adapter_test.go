/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package desktopui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAdapterPromptReturnsChoices(t *testing.T) {
	if !adapterBuilt(BackendGTK) {
		t.Skip("gtk adapter is not part of this build")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "cpak-ui-gtk")
	content := "#!/bin/sh\nif [ \"$1\" = probe ]; then printf 'cpak-ui 1 gtk\\n'; else printf 'allow true false\\n'; fi\n"
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	withoutEmbeddedAdapters(t)
	t.Setenv("CPAK_UI_ADAPTER_DIR", directory)
	result, err := runAdapterPrompt(context.Background(), BackendGTK, adapterPrompt{Title: "Access"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted || !result.Parent || result.Persistent {
		t.Fatalf("unexpected response: %+v", result)
	}
}

func TestEmbeddedAdapterIsMaterializedPrivately(t *testing.T) {
	if !adapterBuilt(BackendGTK) {
		t.Skip("gtk adapter is not part of this build")
	}
	previous := embeddedAdapters
	embeddedAdapters = map[Backend][]byte{BackendGTK: []byte("#!/bin/sh\nprintf 'cpak-ui 1 gtk\\n'\n")}
	t.Cleanup(func() { embeddedAdapters = previous })
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path, err := adapterExecutable(BackendGTK)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Mode().Perm() != 0700 {
		t.Fatalf("adapter mode is %o", stat.Mode().Perm())
	}
}
