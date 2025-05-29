/*
* Copyright (c) 2025 FABRICATORS S.R.L.
* Licensed under the Fabricators Public Access License (FPAL) v1.0
* See https://github.com/fabricatorsltd/FPAL for details.
 */
package types

type RequestParams struct {
	Action      string   `json:"action"`
	ParentAppId string   `json:"parentAppId"`
	Origin      string   `json:"origin"`
	Version     string   `json:"version"`
	Branch      string   `json:"branch"`
	Commit      string   `json:"commit"`
	Release     string   `json:"release"`
	Binary      string   `json:"binary"`
	ExtraArgs   []string `json:"extraArgs"`
}
