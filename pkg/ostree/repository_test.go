/*
 * Copyright (c) 2026 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package ostree_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mirkobrombin/cpak/pkg/gvariant"
	"github.com/mirkobrombin/cpak/pkg/ostree"
	"github.com/mirkobrombin/cpak/pkg/ostree/ostreetest"
)

var (
	keysOnce sync.Once
	trusted  *ostreetest.Key
	stranger *ostreetest.Key
	subkey   *ostreetest.Key
)

func keys(t *testing.T) (*ostreetest.Key, *ostreetest.Key, *ostreetest.Key) {
	t.Helper()
	keysOnce.Do(func() {
		trusted = ostreetest.NewKey()
		stranger = ostreetest.NewKey()
		subkey = ostreetest.NewKey()
	})
	return trusted, stranger, subkey
}

func keyring(t *testing.T, public []byte) *ostree.Keyring {
	t.Helper()
	ring, err := ostree.ParseKeyring(public)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

const appRef = "app/org.example.App/x86_64/stable"

func appFiles() map[string]ostreetest.File {
	return map[string]ostreetest.File{
		"metadata":              {Content: []byte("[Application]\nname=org.example.App\n"), Mode: 0o644},
		"files/bin/app":         {Content: []byte("#!/bin/sh\necho hello\n"), Mode: 0o755},
		"files/lib/libapp.so":   {Content: bytes.Repeat([]byte{0x7f}, 70000), Mode: 0o644},
		"files/lib/libapp.so.1": {Link: "libapp.so"},
		"files/share/empty":     {Dir: true, Mode: 0o700},
		"export/share/applications/org.example.App.desktop": {Content: []byte("[Desktop Entry]\nName=App\n"), Mode: 0o644},
	}
}

func resolve(t *testing.T, repository *ostree.Repository, ref string) (ostree.Commit, error) {
	t.Helper()
	summary, err := repository.FetchSummary(context.Background(), "x86_64")
	if err != nil {
		return ostree.Commit{}, err
	}
	listed, ok := summary.Refs[ref]
	if !ok {
		t.Fatalf("summary does not list %s", ref)
	}
	return repository.FetchCommit(context.Background(), ref, listed.Checksum)
}

func TestIndexedAndPlainSummariesResolveTheSameCommit(t *testing.T) {
	key, _, _ := keys(t)
	for _, indexed := range []bool{true, false} {
		repo := ostreetest.NewRepo(indexed, key)
		want := repo.Commit(appRef, appFiles(), map[string]gvariant.Variant{
			"xa.metadata": {Signature: "s", Value: "[Application]\nname=org.example.App\n"},
		})
		repository := &ostree.Repository{URL: repo.URL(), Keyring: keyring(t, key.Public())}
		commit, err := resolve(t, repository, appRef)
		repo.Close()
		if err != nil {
			t.Fatalf("indexed=%v: %v", indexed, err)
		}
		if commit.Checksum.String() != want {
			t.Fatalf("indexed=%v resolved %s, the repository published %s", indexed, commit.Checksum, want)
		}
		if !strings.Contains(commit.String("xa.metadata"), "org.example.App") {
			t.Fatalf("commit metadata lost xa.metadata: %#v", commit.Metadata)
		}
	}
}

func TestASummarySignedByAnotherKeyIsRefused(t *testing.T) {
	key, other, _ := keys(t)
	repo := ostreetest.NewRepo(true, other)
	defer repo.Close()
	repo.Commit(appRef, appFiles(), nil)
	repository := &ostree.Repository{URL: repo.URL(), Keyring: keyring(t, key.Public())}
	if _, err := repository.FetchSummary(context.Background(), "x86_64"); !errors.Is(err, ostree.ErrVerification) {
		t.Fatalf("a summary signed only by an untrusted key was not refused as unverified: %v", err)
	}
}

func TestAnUnsignedSummaryIsRefused(t *testing.T) {
	key, _, _ := keys(t)
	repo := ostreetest.NewRepo(false, key)
	defer repo.Close()
	repo.Commit(appRef, appFiles(), nil)
	repo.Override["summary.sig"] = nil
	repository := &ostree.Repository{URL: repo.URL(), Keyring: keyring(t, key.Public())}
	_, err := repository.FetchSummary(context.Background(), "x86_64")
	if !errors.Is(err, ostree.ErrVerification) || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("an unsigned summary was not refused as unsigned: %v", err)
	}
}

func subsummaryPath(t *testing.T, index []byte) string {
	t.Helper()
	value, err := gvariant.Parse("(a{s(ayaaya{sv})}a{sv})", index)
	if err != nil {
		t.Fatal(err)
	}
	checksum := value.([]any)[0].([]any)[0].([]any)[1].([]any)[0].([]byte)
	return "summaries/" + hex.EncodeToString(checksum) + ".gz"
}

func TestATamperedSubsummaryIsRefused(t *testing.T) {
	key, _, _ := keys(t)
	repo := ostreetest.NewRepo(true, key)
	defer repo.Close()
	repo.Commit(appRef, appFiles(), nil)
	other := ostreetest.NewRepo(true, key)
	defer other.Close()
	other.Commit(appRef, map[string]ostreetest.File{"metadata": {Content: []byte("evil")}}, nil)

	// Serve another repository's subsummary, itself well formed, under the
	// name the signed index gives this one's.
	repo.Override[subsummaryPath(t, repo.Object("summary.idx"))] = other.Object(subsummaryPath(t, other.Object("summary.idx")))
	repository := &ostree.Repository{URL: repo.URL(), Keyring: keyring(t, key.Public())}
	if _, err := repository.FetchSummary(context.Background(), "x86_64"); !errors.Is(err, ostree.ErrVerification) {
		t.Fatalf("a subsummary that does not match the signed index was not refused as unverified: %v", err)
	}
}

func TestACommitSignedByAnotherKeyIsRefused(t *testing.T) {
	key, other, _ := keys(t)
	repo := ostreetest.NewRepo(true, key)
	defer repo.Close()
	checksum := repo.Commit(appRef, appFiles(), nil)
	commit := repo.Object(ostreetest.ObjectPath(checksum, "commit"))
	forged, err := gvariant.Encode("a{sv}", []any{[]any{"ostree.gpgsigs", gvariant.Variant{Signature: "aay", Value: []any{other.Sign(commit)}}}})
	if err != nil {
		t.Fatal(err)
	}
	repo.Override[ostreetest.ObjectPath(checksum, "commitmeta")] = forged
	repository := &ostree.Repository{URL: repo.URL(), Keyring: keyring(t, key.Public())}
	if _, err := resolve(t, repository, appRef); !errors.Is(err, ostree.ErrVerification) {
		t.Fatalf("a commit without a trusted signature was not refused as unverified: %v", err)
	}
}

func TestACommitBoundToAnotherRefIsRefused(t *testing.T) {
	key, _, _ := keys(t)
	repo := ostreetest.NewRepo(true, key)
	defer repo.Close()
	other := repo.Commit("app/org.example.Other/x86_64/stable", appFiles(), nil)
	repository := &ostree.Repository{URL: repo.URL(), Keyring: keyring(t, key.Public())}
	checksum, err := ostree.ParseChecksum(other)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.FetchCommit(context.Background(), appRef, checksum)
	if !errors.Is(err, ostree.ErrVerification) || !strings.Contains(err.Error(), "was not made for") {
		t.Fatalf("a commit bound to another ref was accepted for %s: %v", appRef, err)
	}
}

func TestASubkeyBoundByThePrimaryKeySigns(t *testing.T) {
	key, _, sub := keys(t)
	repo := ostreetest.NewRepo(true, sub)
	defer repo.Close()
	repo.Commit(appRef, appFiles(), nil)
	repository := &ostree.Repository{URL: repo.URL(), Keyring: keyring(t, key.PublicWithSubkey(sub))}
	if _, err := resolve(t, repository, appRef); err != nil {
		t.Fatalf("a signature by a bound subkey was refused: %v", err)
	}
}

func TestAnUnboundSubkeyDoesNotSign(t *testing.T) {
	key, _, sub := keys(t)
	repo := ostreetest.NewRepo(true, sub)
	defer repo.Close()
	repo.Commit(appRef, appFiles(), nil)
	ring := keyring(t, key.PublicWithUnboundSubkey(sub))
	if len(ring.Fingerprints()) != 1 {
		t.Fatalf("an unbound subkey joined the keyring: %v", ring.Fingerprints())
	}
	repository := &ostree.Repository{URL: repo.URL(), Keyring: ring}
	if _, err := repository.FetchSummary(context.Background(), "x86_64"); err == nil {
		t.Fatal("a signature by a subkey its primary never bound was accepted")
	}
}

func TestTreeListsEveryPathInOrder(t *testing.T) {
	key, _, _ := keys(t)
	repo := ostreetest.NewRepo(true, key)
	defer repo.Close()
	repo.Commit(appRef, appFiles(), nil)
	repository := &ostree.Repository{URL: repo.URL(), Keyring: keyring(t, key.Public()), Parallel: 4}
	commit, err := resolve(t, repository, appRef)
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.Tree(context.Background(), commit)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.Tree(context.Background(), commit)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatal("two walks of one commit listed different trees")
	}
	seen := map[string]bool{}
	for index, entry := range first {
		if entry.Path != second[index].Path {
			t.Fatalf("two walks of one commit listed %s and %s at %d", entry.Path, second[index].Path, index)
		}
		if parent := parentOf(entry.Path); parent != "" && !seen[parent] {
			t.Fatalf("%s was listed before its directory", entry.Path)
		}
		seen[entry.Path] = true
	}
	for _, want := range []string{"metadata", "files/bin/app", "files/lib/libapp.so.1", "files/share/empty"} {
		if !seen[want] {
			t.Fatalf("the tree does not list %s", want)
		}
	}
}

func parentOf(name string) string {
	index := strings.LastIndex(name, "/")
	if index < 0 {
		return ""
	}
	return name[:index]
}

func fileContent(t *testing.T, repository *ostree.Repository, checksum ostree.Checksum) (ostree.FileHeader, []byte, error) {
	t.Helper()
	var object bytes.Buffer
	size, err := repository.FetchFileObject(context.Background(), checksum, &object)
	if err != nil {
		return ostree.FileHeader{}, nil, err
	}
	header, reader, err := ostree.OpenFileObject(checksum, bytes.NewReader(object.Bytes()), size)
	if err != nil {
		return header, nil, err
	}
	content, err := io.ReadAll(reader)
	return header, content, err
}

func TestFileObjectsAreVerified(t *testing.T) {
	key, _, _ := keys(t)
	repo := ostreetest.NewRepo(true, key)
	defer repo.Close()
	files := appFiles()
	repo.Commit(appRef, files, nil)
	repository := &ostree.Repository{URL: repo.URL(), Keyring: keyring(t, key.Public())}

	library := ostree.Checksum(ostreetest.FileChecksum(files["files/lib/libapp.so"]))
	header, content, err := fileContent(t, repository, library)
	if err != nil {
		t.Fatal(err)
	}
	if !header.IsRegular() || !bytes.Equal(content, files["files/lib/libapp.so"].Content) {
		t.Fatal("a file object did not read back as the file that was committed")
	}

	link := ostree.Checksum(ostreetest.FileChecksum(files["files/lib/libapp.so.1"]))
	header, _, err = fileContent(t, repository, link)
	if err != nil {
		t.Fatal(err)
	}
	if !header.IsSymlink() || header.Target != "libapp.so" {
		t.Fatalf("a symlink object read back as %+v", header)
	}
}

func TestAFileObjectWithOtherContentFailsAtItsEnd(t *testing.T) {
	key, _, _ := keys(t)
	repo := ostreetest.NewRepo(true, key)
	defer repo.Close()
	files := appFiles()
	repo.Commit(appRef, files, nil)
	binary := ostree.Checksum(ostreetest.FileChecksum(files["files/bin/app"]))
	other := ostree.Checksum(ostreetest.FileChecksum(files["metadata"]))
	repo.Override[ostreetest.ObjectPath(binary.String(), "filez")] = repo.Object(ostreetest.ObjectPath(other.String(), "filez"))
	repository := &ostree.Repository{URL: repo.URL(), Keyring: keyring(t, key.Public())}
	if _, _, err := fileContent(t, repository, binary); !errors.Is(err, ostree.ErrVerification) {
		t.Fatalf("a file object holding another file's content was not refused as unverified: %v", err)
	}
}

func TestATreeEntryThatEscapesItsDirectoryIsRefused(t *testing.T) {
	key, _, _ := keys(t)
	repo := ostreetest.NewRepo(true, key)
	defer repo.Close()
	repo.Commit(appRef, map[string]ostreetest.File{"..": {Content: []byte("x")}}, nil)
	repository := &ostree.Repository{URL: repo.URL(), Keyring: keyring(t, key.Public())}
	commit, err := resolve(t, repository, appRef)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Tree(context.Background(), commit); err == nil {
		t.Fatal("a tree entry named .. was accepted")
	}
}

func TestPlainHTTPIsRefusedOutsideLoopback(t *testing.T) {
	key, _, _ := keys(t)
	repository := &ostree.Repository{URL: "http://dl.example.org/repo/", Keyring: keyring(t, key.Public())}
	_, err := repository.FetchSummary(context.Background(), "x86_64")
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("a repository over plain HTTP was not refused: %v", err)
	}
}

func TestAnExpiredSignatureIsRefused(t *testing.T) {
	key, _, _ := keys(t)
	ring := keyring(t, key.Public())
	data := []byte("summary")
	made := time.Now().Add(-48 * time.Hour)
	if err := ring.VerifyDetached(data, [][]byte{key.SignAt(data, made, 24*time.Hour)}, time.Now()); err == nil {
		t.Fatal("a signature past its expiry was accepted")
	}
	if err := ring.VerifyDetached(data, [][]byte{key.SignAt(data, made, 72*time.Hour)}, time.Now()); err != nil {
		t.Fatalf("a signature within its lifetime was refused: %v", err)
	}
}

func TestARepositoryThatDoesNotAnswerIsUnavailable(t *testing.T) {
	key, _, _ := keys(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	repository := &ostree.Repository{URL: server.URL, Keyring: keyring(t, key.Public())}
	_, err := repository.FetchSummary(context.Background(), "x86_64")
	if !errors.Is(err, ostree.ErrUnavailable) || errors.Is(err, ostree.ErrVerification) {
		t.Fatalf("a repository answering 503 was reported as %v", err)
	}

	server.Close()
	if _, err := repository.FetchSummary(context.Background(), "x86_64"); !errors.Is(err, ostree.ErrUnavailable) {
		t.Fatalf("a repository nobody listens on was reported as %v", err)
	}
}
