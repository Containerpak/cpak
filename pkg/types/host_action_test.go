/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package types

import (
	"reflect"
	"testing"
)

func TestValidateHostActionsRejectsUnknownAndDuplicateCapabilities(t *testing.T) {
	for _, grants := range [][]HostActionGrant{
		{{Provider: "commands", Capabilities: []string{"exec"}}},
		{{Provider: HostActionProviderContainers, Capabilities: []string{"all"}}},
		{{Provider: HostActionProviderContainers, Capabilities: []string{HostActionContainersRead, HostActionContainersRead}}},
		{
			{Provider: HostActionProviderContainers, Capabilities: []string{HostActionContainersRead}},
			{Provider: HostActionProviderContainers, Capabilities: []string{HostActionContainersManageOwned}},
		},
	} {
		if err := ValidateHostActions(grants); err == nil {
			t.Fatalf("invalid host actions were accepted: %+v", grants)
		}
	}
}

func TestIntersectHostActionsCannotEscalateCapabilities(t *testing.T) {
	parent := []HostActionGrant{{
		Provider:     HostActionProviderContainers,
		Capabilities: []string{HostActionContainersRead, HostActionContainersManageOwned},
	}}
	child := []HostActionGrant{{
		Provider:     HostActionProviderContainers,
		Capabilities: []string{HostActionContainersExecOwned, HostActionContainersRead},
	}}
	want := []HostActionGrant{{
		Provider:     HostActionProviderContainers,
		Capabilities: []string{HostActionContainersRead},
	}}
	if got := IntersectHostActions(parent, child); !reflect.DeepEqual(got, want) {
		t.Fatalf("host action intersection: got %+v, want %+v", got, want)
	}
}

func TestDecodeHostActionsRejectsUnknownFields(t *testing.T) {
	_, err := DecodeHostActionsJSON([]byte(`[{"provider":"containers","capabilities":["read"],"command":"id"}]`))
	if err == nil {
		t.Fatal("unknown host action field was accepted")
	}
}
