/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import (
	"debug/elf"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

const nixStorePath = "/nix/store"

func nixRuntimeStoreRoots(binary string) ([]string, error) {
	if nixStoreRoot(binary) == "" {
		return nil, nil
	}
	paths, err := executableRuntimePaths(binary)
	if err != nil {
		return nil, fmt.Errorf("inspect cpak runtime: %w", err)
	}
	return nixStoreRoots(binary, paths), nil
}

func executableRuntimePaths(binary string) ([]string, error) {
	executable, err := elf.Open(binary)
	if err != nil {
		return nil, err
	}
	defer executable.Close()

	paths := []string{}
	for _, program := range executable.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		encoded, readErr := io.ReadAll(io.LimitReader(program.Open(), 4096))
		if readErr != nil {
			return nil, readErr
		}
		paths = append(paths, strings.TrimRight(string(encoded), "\x00"))
	}
	for _, tag := range []elf.DynTag{elf.DT_NEEDED, elf.DT_RPATH, elf.DT_RUNPATH} {
		values, readErr := executable.DynString(tag)
		if readErr != nil {
			return nil, readErr
		}
		for _, value := range values {
			paths = append(paths, strings.Split(value, ":")...)
		}
	}
	return paths, nil
}

func nixStoreRoots(binary string, runtimePaths []string) []string {
	roots := map[string]struct{}{}
	for _, candidate := range append([]string{binary}, runtimePaths...) {
		if root := nixStoreRoot(candidate); root != "" {
			roots[root] = struct{}{}
		}
	}
	result := make([]string, 0, len(roots))
	for root := range roots {
		result = append(result, root)
	}
	sort.Strings(result)
	return result
}

func nixStoreRoot(path string) string {
	clean := filepath.Clean(path)
	relative, err := filepath.Rel(nixStorePath, clean)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ""
	}
	entry := strings.SplitN(relative, string(filepath.Separator), 2)[0]
	if entry == "" || entry == "." || entry == ".." {
		return ""
	}
	return filepath.Join(nixStorePath, entry)
}
