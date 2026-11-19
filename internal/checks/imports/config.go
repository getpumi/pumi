// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package imports

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// Config is .pumi/imports.json (decision 0112): where the api module, each
// component, internal/shared, internal/checks and cmd are, and the
// third-party modules each of them may import (decisions 0064 and 0125). internal/shared has no list,
// because it imports only the standard library and the api module (decision
// 0063). Every path is relative to the root of the core module. Adding a
// module to a list loosens the gate and is reviewed as such (decision 0064).
type Config struct {
	API        Group       `json:"api"`
	Components []Component `json:"components"`
	Shared     Shared      `json:"shared"`
	Checks     Group       `json:"checks"`
	Commands   Group       `json:"commands"`
}

// Component is one component: its name for messages, its directory and the
// third-party modules it may import.
type Component struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Modules []Module `json:"modules"`
}

// Shared is internal/shared, the one shared leaf package.
type Shared struct {
	Path string `json:"path"`
}

// Group is the api module, internal/checks or cmd: its directory and the
// third-party modules its packages may import.
type Group struct {
	Path    string   `json:"path"`
	Modules []Module `json:"modules"`
}

// Module is one third-party module on a list, with the reason it is there.
// JSON has no comments, so the reason is a field (decision 0112).
type Module struct {
	Module string `json:"module"`
	Reason string `json:"reason"`
}

// ParseConfig reads .pumi/imports.json. It rejects unknown fields, so a
// misspelled field is not silently ignored, requires every field, and
// rejects paths that are not clean relative paths or that lie inside one
// another, so each package belongs to at most one place.
func ParseConfig(data []byte) (*Config, error) {
	var cfg Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("data after the JSON object")
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (cfg *Config) validate() error {
	if len(cfg.Components) == 0 {
		return errors.New(`"components" is empty`)
	}
	if err := cfg.validateComponents(); err != nil {
		return err
	}
	for _, g := range []struct {
		name  string
		group Group
	}{
		{"api", cfg.API},
		{"shared", Group{Path: cfg.Shared.Path, Modules: []Module{}}},
		{"checks", cfg.Checks},
		{"commands", cfg.Commands},
	} {
		if strings.TrimSpace(g.group.Path) == "" {
			return fmt.Errorf("%q is empty", g.name+".path")
		}
		if g.group.Modules == nil {
			return fmt.Errorf("%q is missing", g.name+".modules")
		}
		if err := validateModules(g.group.Modules); err != nil {
			return fmt.Errorf("%s: %w", g.name, err)
		}
	}
	return validatePaths(cfg.paths())
}

func (cfg *Config) validateComponents() error {
	names := map[string]bool{}
	for i, c := range cfg.Components {
		switch {
		case strings.TrimSpace(c.Name) == "":
			return fmt.Errorf("component %d: %q is empty", i+1, "name")
		case names[c.Name]:
			return fmt.Errorf("component %d: the name %q is listed twice", i+1, c.Name)
		case strings.TrimSpace(c.Path) == "":
			return fmt.Errorf("component %d: %q is empty", i+1, "path")
		case c.Modules == nil:
			return fmt.Errorf("component %d: %q is missing", i+1, "modules")
		}
		names[c.Name] = true
		if err := validateModules(c.Modules); err != nil {
			return fmt.Errorf("component %s: %w", c.Name, err)
		}
	}
	return nil
}

func validateModules(mods []Module) error {
	seen := map[string]bool{}
	for i, m := range mods {
		for _, f := range []struct{ name, value string }{{"module", m.Module}, {"reason", m.Reason}} {
			if strings.TrimSpace(f.value) == "" {
				return fmt.Errorf("module %d: %q is empty", i+1, f.name)
			}
		}
		if seen[m.Module] {
			return fmt.Errorf("the module %s is listed twice", m.Module)
		}
		seen[m.Module] = true
	}
	return nil
}

// paths returns every path in the configuration, in the order of the file.
func (cfg *Config) paths() []string {
	paths := []string{cfg.API.Path}
	for _, c := range cfg.Components {
		paths = append(paths, c.Path)
	}
	return append(paths, cfg.Shared.Path, cfg.Checks.Path, cfg.Commands.Path)
}

// validatePaths rejects a path that is not clean and relative, and a path
// that is the same as or inside an earlier one, or contains one.
func validatePaths(paths []string) error {
	for i, p := range paths {
		if path.Clean(p) != p || path.IsAbs(p) || p == "." || p == ".." || strings.HasPrefix(p, "../") {
			return fmt.Errorf("%q is not a clean relative path", p)
		}
		for _, q := range paths[:i] {
			switch {
			case within(p, q):
				return fmt.Errorf("%q is inside %q", p, q)
			case within(q, p):
				return fmt.Errorf("%q is inside %q", q, p)
			}
		}
	}
	return nil
}

// within reports whether the slash-separated path p is dir or inside it.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}
