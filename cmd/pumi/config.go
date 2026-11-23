// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/encoding/prototext"

	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/kernel/workflow"
)

// config is the configuration file of pumi, read (decision 0207).
type config struct {
	file *configv1.Config
	// dir is the absolute path of the file's directory, against which its
	// relative paths are read.
	dir string
}

// readConfig reads the configuration file at path, in protobuf text
// format, and refuses one with an unknown field or without the workflow,
// the adapter, the trunk or the project's name.
func readConfig(path string) (*config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("cannot open the directory of the configuration file: %w", err)
	}
	defer func() { _ = root.Close() }()
	src, err := root.ReadFile(filepath.Base(abs))
	if err != nil {
		return nil, fmt.Errorf("cannot read the configuration file: %w", err)
	}
	file := &configv1.Config{}
	if err := prototext.Unmarshal(src, file); err != nil {
		return nil, fmt.Errorf("cannot parse the configuration file %s: %w", path, err)
	}
	var missing []error
	for _, f := range []struct{ name, value string }{
		{"workflow", file.GetWorkflow()}, {"adapter", file.GetAdapter()}, {"trunk", file.GetTrunk()},
		{"project.name", file.GetProject().GetName()},
	} {
		if f.value == "" {
			missing = append(missing, fmt.Errorf("the configuration file %s sets no %s", path, f.name))
		}
	}
	if err := errors.Join(missing...); err != nil {
		return nil, err
	}
	return &config{file: file, dir: filepath.Dir(abs)}, nil
}

// path returns p, a path of the file, as an absolute path.
func (c *config) path(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.dir, p)
}

// workflow loads the workflow file.
func (c *config) workflow() (*workflow.Workflow, error) {
	p := c.path(c.file.GetWorkflow())
	return workflow.Load(os.DirFS(filepath.Dir(p)), filepath.Base(p))
}

// plane returns the configuration of the embedded control plane. The hard
// caps are the release's defaults when the file sets none.
func (c *config) plane() controlplane.Config {
	caps := policy.DefaultHardCaps()
	if h := c.file.GetHardCaps(); h != nil {
		caps = policy.Caps{Tokens: h.GetTokens(), CostMicrodollars: h.GetCostMicrodollars(), Seconds: h.GetSeconds()}
	}
	return controlplane.Config{
		Settings: controlplane.Settings{
			MaxAttempts: c.file.GetSettings().GetMaxAttempts(),
			Parallelism: int(c.file.GetSettings().GetParallelism()),
		},
		HardCaps: caps,
		Project:  controlplane.Project{Name: c.file.GetProject().GetName(), ProtectedPaths: c.file.GetProject().GetProtectedPaths()},
	}
}
