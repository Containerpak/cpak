/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package cmd

import (
	"reflect"
	"testing"
)

func TestOrchestrationOrder(t *testing.T) {
	got, err := orchestrationOrder(
		[]string{"api", "database", "worker"},
		[]string{"api=database", "worker=api"},
	)
	if err != nil {
		t.Fatalf("orchestration order failed: %v", err)
	}
	if want := []string{"database", "api", "worker"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestOrchestrationOrderRejectsCycles(t *testing.T) {
	if _, err := orchestrationOrder([]string{"a", "b"}, []string{"a=b", "b=a"}); err == nil {
		t.Fatal("expected cycle validation to fail")
	}
}
