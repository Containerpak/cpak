package storaged

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestStorageRefusesDeduplicatedLocks(t *testing.T) {
	for _, pair := range [][2]string{
		{"blocks/.objects/lock", "blocks/.objects/transaction.lock"},
		{"layers/first/.fvs2/lock", "layers/second/.fvs2/lock"},
	} {
		t.Run(pair[0], func(t *testing.T) {
			root := t.TempDir()
			first, second := filepath.Join(root, pair[0]), filepath.Join(root, pair[1])
			for _, path := range []string{first, second} {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			lock, err := os.OpenFile(first, os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := os.Link(first, second); err != nil {
				t.Fatal(err)
			}
			if err := unix.Flock(int(lock.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			if _, err := NewFVS(root, filepath.Join(root, "driver")); err == nil || !strings.Contains(err.Error(), "hardlinked") {
				t.Fatalf("shared lock inode did not fail before driver startup: %v", err)
			}
			if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
				t.Fatalf("validation left a lock behind: %v", err)
			}
		})
	}
}
