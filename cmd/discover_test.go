/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cmd

import (
	"testing"

	"github.com/mirkobrombin/cpak/pkg/bootstrap"
	"github.com/mirkobrombin/cpak/pkg/catalog"
	"github.com/mirkobrombin/cpak/pkg/types"
)

func TestCatalogVersionPrefersThePackageVersion(t *testing.T) {
	item := catalog.Package{Metadata: bootstrap.Metadata{Version: "1.2.3", Ref: "0123456789abcdef"}}
	if got := catalogVersion(item); got != "1.2.3" {
		t.Fatalf("catalog version: %q", got)
	}
}

func TestCatalogVersionFallsBackToTheCommit(t *testing.T) {
	item := catalog.Package{Metadata: bootstrap.Metadata{Ref: "0123456789abcdef"}}
	if got := catalogVersion(item); got != "0123456789ab" {
		t.Fatalf("catalog version: %q", got)
	}
}

func TestApplicationVersionPreservesTheInstalledReference(t *testing.T) {
	app := types.Application{Version: "fallback", Branch: "main", Commit: "0123456789abcdef", Release: "v1.2.3"}
	if got := applicationVersion(app); got != "v1.2.3" {
		t.Fatalf("application version: %q", got)
	}
}
