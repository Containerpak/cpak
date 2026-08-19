/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// A manifest names the branch, the release or the commit its dependency comes
// from, and cpak used to put that name straight into two places at once: a URL
// it fetched, and a path it wrote under. Neither had a check.
//
// A name carrying ".." walked out of the manifest cache, because filepath.Join
// normalises and then MkdirAll obligingly built the tree. A name carrying "?"
// split into a query string for url.Parse while filepath.Join went on treating
// the rest as path, so the file was written somewhere else again, under a name
// the same party chose. The result was a write of arbitrary content to
// arbitrary paths during an ordinary install, which is a login away from
// running on the host.
//
// What follows is deliberately narrower than what git accepts. A package needs
// a branch name, a tag or a commit, and none of those need any of the
// characters that made this possible.

// gitReferencePattern is the shape a reference may have.
var gitReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,254}$`)

// validateGitReference refuses a reference cpak will not put in a URL or use to
// build a path.
func validateGitReference(reference string) error {
	if !gitReferencePattern.MatchString(reference) {
		return fmt.Errorf("invalid git reference: %q", reference)
	}
	// The pattern admits dots, because tags are full of them, so the one
	// sequence that matters is refused on its own.
	if strings.Contains(reference, "..") {
		return fmt.Errorf("invalid git reference: %q", reference)
	}
	if strings.Contains(reference, "//") || strings.HasSuffix(reference, "/") {
		return fmt.Errorf("invalid git reference: %q", reference)
	}
	if strings.HasSuffix(reference, ".lock") {
		return fmt.Errorf("invalid git reference: %q", reference)
	}
	return nil
}

// singlePathComponent answers the name a caller asked for, and refuses anything
// that is more than a name. What cpak fetches from a repository is one file it
// chose itself, so a value that navigates is a value nobody meant to send.
func singlePathComponent(name string) (string, error) {
	cleaned := filepath.Clean(name)
	if cleaned != name || cleaned == "" || cleaned == "." || cleaned == ".." {
		return "", fmt.Errorf("invalid file name: %q", name)
	}
	if strings.ContainsAny(cleaned, `/\`) || strings.ContainsRune(cleaned, 0) {
		return "", fmt.Errorf("invalid file name: %q", name)
	}
	return cleaned, nil
}

// containedPath joins a relative path onto a root and answers only when the
// result is still under that root.
//
// It is the check that would have stopped this on its own, and it is kept even
// though the two above already do, because a path that escapes its root is
// worth refusing wherever it is noticed rather than wherever it was formed.
func containedPath(root, relative string) (string, error) {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", root, err)
	}
	candidate := filepath.Join(absoluteRoot, relative)
	if candidate != absoluteRoot && !strings.HasPrefix(candidate, absoluteRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes %q", relative, root)
	}
	return candidate, nil
}
