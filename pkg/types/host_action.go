/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
 */
package types

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

const (
	HostActionProviderContainers = "containers"

	HostActionContainersRead        = "read"
	HostActionContainersManageOwned = "manage-owned"
	HostActionContainersExecOwned   = "exec-owned"
)

type HostActionGrant struct {
	Provider     string   `json:"provider" jsonschema:"enum=containers,description=Built-in host service provider"`
	Capabilities []string `json:"capabilities" jsonschema:"minItems=1,description=Provider capabilities"`
}

func ValidateHostActions(grants []HostActionGrant) error {
	providers := map[string]bool{}
	for _, grant := range grants {
		if grant.Provider != HostActionProviderContainers {
			return fmt.Errorf("unsupported host action provider: %s", grant.Provider)
		}
		if providers[grant.Provider] {
			return fmt.Errorf("host action provider is declared more than once: %s", grant.Provider)
		}
		providers[grant.Provider] = true
		if len(grant.Capabilities) == 0 {
			return fmt.Errorf("host action provider has no capabilities: %s", grant.Provider)
		}
		capabilities := map[string]bool{}
		for _, capability := range grant.Capabilities {
			if !validContainerCapability(capability) {
				return fmt.Errorf("unsupported %s capability: %s", grant.Provider, capability)
			}
			if capabilities[capability] {
				return fmt.Errorf("host action capability is declared more than once: %s.%s", grant.Provider, capability)
			}
			capabilities[capability] = true
		}
	}
	return nil
}

func HostActionCapabilities(grants []HostActionGrant, provider string) map[string]bool {
	for _, grant := range grants {
		if grant.Provider != provider {
			continue
		}
		result := make(map[string]bool, len(grant.Capabilities))
		for _, capability := range grant.Capabilities {
			result[capability] = true
		}
		return result
	}
	return nil
}

func IntersectHostActions(parent, child []HostActionGrant) []HostActionGrant {
	var result []HostActionGrant
	for _, requested := range child {
		allowed := HostActionCapabilities(parent, requested.Provider)
		capabilities := []string{}
		for _, capability := range requested.Capabilities {
			if allowed[capability] {
				capabilities = append(capabilities, capability)
			}
		}
		if len(capabilities) == 0 {
			continue
		}
		sort.Strings(capabilities)
		result = append(result, HostActionGrant{Provider: requested.Provider, Capabilities: capabilities})
	}
	return result
}

func DecodeHostActionsJSON(data []byte) ([]HostActionGrant, error) {
	grants := []HostActionGrant{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&grants); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("host actions contain multiple JSON values")
	}
	if err := ValidateHostActions(grants); err != nil {
		return nil, err
	}
	return grants, nil
}

func validContainerCapability(capability string) bool {
	switch capability {
	case HostActionContainersRead, HostActionContainersManageOwned, HostActionContainersExecOwned:
		return true
	default:
		return false
	}
}
