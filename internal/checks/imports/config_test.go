// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package imports

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

const goodConfig = `{
  "api": {"path": "api", "modules": [
    {"module": "example.com/proto", "reason": "The api module needs it."}
  ]},
  "components": [
    {"name": "kernel", "path": "internal/kernel", "modules": [
      {"module": "example.com/lib", "reason": "The kernel needs it."}
    ]},
    {"name": "runner", "path": "internal/runner", "modules": []}
  ],
  "shared": {"path": "internal/shared"},
  "checks": {"path": "internal/checks", "modules": []},
  "commands": {"path": "cmd", "modules": []}
}`

func TestParseConfig(t *testing.T) {
	got, err := ParseConfig([]byte(goodConfig))
	if err != nil {
		t.Fatal(err)
	}
	want := &Config{
		API: Group{Path: "api", Modules: []Module{{Module: "example.com/proto", Reason: "The api module needs it."}}},
		Components: []Component{
			{Name: "kernel", Path: "internal/kernel", Modules: []Module{{Module: "example.com/lib", Reason: "The kernel needs it."}}},
			{Name: "runner", Path: "internal/runner", Modules: []Module{}},
		},
		Shared:   Shared{Path: "internal/shared"},
		Checks:   Group{Path: "internal/checks", Modules: []Module{}},
		Commands: Group{Path: "cmd", Modules: []Module{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseConfig() = %+v, want %+v", got, want)
	}
}

// TestParseConfigReadsTheRepositoryFile parses the repository's own
// .pumi/imports.json and checks that it lists the five components of
// decision 0105.
func TestParseConfigReadsTheRepositoryFile(t *testing.T) {
	data, err := os.ReadFile("../../../.pumi/imports.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range cfg.Components {
		paths = append(paths, c.Path)
	}
	want := []string{"internal/kernel", "internal/controlplane", "internal/runner", "internal/gateway", "internal/cli"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("component paths = %q, want %q", paths, want)
	}
}

func TestParseConfigRejectsNoComponents(t *testing.T) {
	data := `{"api": {"path": "api", "modules": []}, "components": [], "shared": {"path": "s"}, "checks": {"path": "c", "modules": []}, "commands": {"path": "cmd", "modules": []}}`
	_, err := ParseConfig([]byte(data))
	if err == nil || !strings.Contains(err.Error(), `"components" is empty`) {
		t.Errorf("ParseConfig() error = %v, want one about empty components", err)
	}
}

func TestParseConfigRejectsBadFiles(t *testing.T) {
	tests := []struct {
		name, old, new, want string
	}{
		{"unknown field", `"components": [`, `"extra": 1, "components": [`, `unknown field "extra"`},
		{"missing api", `"api": {"path": "api", "modules": [
    {"module": "example.com/proto", "reason": "The api module needs it."}
  ]},`, ``, `"api.path" is empty`},
		{"api as a string", `"api": {"path": "api", "modules": [
    {"module": "example.com/proto", "reason": "The api module needs it."}
  ]},`, `"api": "api",`, `cannot unmarshal string`},
		{"missing api modules", `{"path": "api", "modules": [
    {"module": "example.com/proto", "reason": "The api module needs it."}
  ]}`, `{"path": "api"}`, `"api.modules" is missing`},
		{"empty api reason", `"reason": "The api module needs it."`, `"reason": ""`, `api: module 1: "reason" is empty`},
		{"missing shared", `"shared": {"path": "internal/shared"},`, ``, `"shared.path" is empty`},
		{"missing checks", `"checks": {"path": "internal/checks", "modules": []},`, ``, `"checks.path" is empty`},
		{"missing commands", `,
  "commands": {"path": "cmd", "modules": []}`, ``, `"commands.path" is empty`},
		{"modules on shared", `{"path": "internal/shared"}`, `{"path": "internal/shared", "modules": []}`, `unknown field "modules"`},
		{"empty name", `"name": "runner"`, `"name": ""`, `component 2: "name" is empty`},
		{"duplicate name", `"name": "runner"`, `"name": "kernel"`, `component 2: the name "kernel" is listed twice`},
		{"missing modules", `"path": "internal/runner", "modules": []`, `"path": "internal/runner"`, `component 2: "modules" is missing`},
		{"empty reason", `"reason": "The kernel needs it."`, `"reason": " "`, `component kernel: module 1: "reason" is empty`},
		{"empty module", `"module": "example.com/lib"`, `"module": ""`, `component kernel: module 1: "module" is empty`},
		{"duplicate module", `{"module": "example.com/lib", "reason": "The kernel needs it."}`, `{"module": "example.com/lib", "reason": "a"}, {"module": "example.com/lib", "reason": "b"}`, `component kernel: the module example.com/lib is listed twice`},
		{"absolute path", `"path": "internal/runner"`, `"path": "/internal/runner"`, `"/internal/runner" is not a clean relative path`},
		{"dot path", `"path": "internal/runner"`, `"path": "internal/../runner"`, `"internal/../runner" is not a clean relative path`},
		{"root path", `"path": "internal/runner"`, `"path": "."`, `"." is not a clean relative path`},
		{"overlapping paths", `"path": "internal/runner"`, `"path": "internal/kernel/runner"`, `"internal/kernel/runner" is inside "internal/kernel"`},
		{"same path", `"path": "cmd"`, `"path": "internal/checks"`, `"internal/checks" is inside "internal/checks"`},
		{"trailing data", `"modules": []}
}`, `"modules": []}
} {}`, `data after the JSON object`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(goodConfig, tt.old) {
				t.Fatalf("goodConfig does not contain %q", tt.old)
			}
			data := strings.Replace(goodConfig, tt.old, tt.new, 1)
			_, err := ParseConfig([]byte(data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ParseConfig() error = %v, want one that contains %q", err, tt.want)
			}
		})
	}
}
