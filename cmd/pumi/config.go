// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/reflect/protoreflect"

	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/kernel"
	"getpumi.dev/pumi/internal/kernel/workflow"
	"getpumi.dev/pumi/internal/runner"
)

// config is the configuration file of pumi, read (decision 0207).
type config struct {
	file *configv1.Config
	// dir is the absolute path of the file's directory, against which its
	// relative paths are read.
	dir string
}

// readConfig reads the configuration file at path, in protobuf text
// format, and refuses one with an unknown field, without the workflow, the
// trunk or the project's name, with the replaced adapter field or gateway
// section, with a model configuration that checkModels refuses, or with a
// project section that checkProject refuses.
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
		{"workflow", file.GetWorkflow()}, {"trunk", file.GetTrunk()},
		{"project.name", file.GetProject().GetName()},
	} {
		if f.value == "" {
			missing = append(missing, fmt.Errorf("the configuration file %s sets no %s", path, f.name))
		}
	}
	// The replaced fields are read by name, because their getters are
	// deprecated.
	for _, name := range []protoreflect.Name{"adapter", "gateway"} {
		if file.ProtoReflect().Has(file.ProtoReflect().Descriptor().Fields().ByName(name)) {
			missing = append(missing, fmt.Errorf("the configuration file %s sets %s, which models replaces: the adapters, their API key variables and the model roles are in models (decision 0245)", path, name))
		}
	}
	missing = append(missing, checkModels(path, file.GetModels())...)
	missing = append(missing, checkProject(path, file.GetProject())...)
	if err := errors.Join(missing...); err != nil {
		return nil, err
	}
	return &config{file: file, dir: filepath.Dir(abs)}, nil
}

// checkModels returns the errors of the model configuration m of the file
// at path (decisions 0240, 0245 and 0253): the errors of checkAdapters and
// checkRoles, and a check adapter that it does not list.
func checkModels(path string, m *configv1.Models) []error {
	if m == nil {
		return []error{fmt.Errorf("the configuration file %s sets no models (decision 0245)", path)}
	}
	errs := checkAdapters(path, m.GetAdapters())
	errs = append(errs, checkRoles(path, m)...)
	if _, ok := m.GetAdapters()[m.GetCheckAdapter()]; !ok {
		errs = append(errs, fmt.Errorf("the configuration file %s: models.check_adapter %q is not an adapter that models.adapters lists (decision 0253)", path, m.GetCheckAdapter()))
	}
	return errs
}

// checkAdapters returns the errors of the adapters of the file at path: it
// needs one at least, each with an executable, a SHA-256 in lowercase
// hexadecimal and a credential mode, with the variable that holds the key
// in the API key mode and none in the sign-in mode (decisions 0240 and
// 0245).
func checkAdapters(path string, adapters map[string]*configv1.Adapter) []error {
	if len(adapters) == 0 {
		return []error{fmt.Errorf("the configuration file %s: models.adapters lists no adapter", path)}
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(adapters)) {
		for _, p := range adapterProblems(adapters[name]) {
			errs = append(errs, fmt.Errorf("the configuration file %s: the adapter %q: %s", path, name, p))
		}
	}
	return errs
}

// adapterProblems returns what is wrong with the adapter entry a.
func adapterProblems(a *configv1.Adapter) []string {
	var problems []string
	if a.GetExecutable() == "" {
		problems = append(problems, "it has no executable")
	}
	if !isSHA256(a.GetSha256()) {
		problems = append(problems, fmt.Sprintf("its sha256 %q is not 64 lowercase hexadecimal digits", a.GetSha256()))
	}
	switch a.GetCredentialMode() {
	case configv1.Adapter_CREDENTIAL_MODE_API_KEY:
		if a.GetApiKeyEnv() == "" {
			problems = append(problems, "it is in the API key mode and names no api_key_env (decision 0240)")
		}
	case configv1.Adapter_CREDENTIAL_MODE_SIGN_IN:
		if a.GetApiKeyEnv() != "" {
			problems = append(problems, fmt.Sprintf("it is in the sign-in mode and names the key variable %s; the sign-in mode uses no API key (decision 0240)", a.GetApiKeyEnv()))
		}
	case configv1.Adapter_CREDENTIAL_MODE_UNSPECIFIED:
		problems = append(problems, "it sets no credential_mode (decision 0240)")
	default:
		problems = append(problems, fmt.Sprintf("its credential_mode %v is unknown", a.GetCredentialMode()))
	}
	return problems
}

// checkRoles returns the errors of the model roles of m in the file at
// path: each needs an adapter that m lists and a model, and a fallback,
// when it has one, needs an adapter that m lists and a model, and must
// differ from the role (decisions 0245 and 0246).
func checkRoles(path string, m *configv1.Models) []error {
	known := func(adapter string) bool { _, ok := m.GetAdapters()[adapter]; return ok }
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(m.GetRoles())) {
		r := m.GetRoles()[name]
		var problems []string
		switch {
		case r.GetAdapter() == "":
			problems = append(problems, "it names no adapter (decision 0245)")
		case !known(r.GetAdapter()):
			problems = append(problems, fmt.Sprintf("it names the adapter %q, which models.adapters does not list", r.GetAdapter()))
		}
		if r.GetModel() == "" {
			problems = append(problems, "it names no model")
		}
		if f := r.GetFallback(); f != nil {
			switch {
			case !known(f.GetAdapter()):
				problems = append(problems, fmt.Sprintf("its fallback names the adapter %q, which models.adapters does not list", f.GetAdapter()))
			case f.GetModel() == "":
				problems = append(problems, "its fallback names no model")
			case f.GetAdapter() == r.GetAdapter() && f.GetModel() == r.GetModel():
				problems = append(problems, "its fallback is the role's own adapter and model")
			}
		}
		for _, p := range problems {
			errs = append(errs, fmt.Errorf("the configuration file %s: the model role %q: %s", path, name, p))
		}
	}
	return errs
}

// isSHA256 reports whether s is a SHA-256 in lowercase hexadecimal.
func isSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// adapters returns the adapters of the model configuration for the
// runner, with absolute paths (decision 0245).
func (c *config) adapters() map[string]runner.Adapter {
	out := map[string]runner.Adapter{}
	for name, a := range c.file.GetModels().GetAdapters() {
		out[name] = runner.Adapter{
			Path: c.path(a.GetExecutable()), SHA256: a.GetSha256(),
			SignIn: a.GetCredentialMode() == configv1.Adapter_CREDENTIAL_MODE_SIGN_IN,
		}
	}
	return out
}

// roleAdapters returns the name of the adapter of each model role.
func (c *config) roleAdapters() map[string]string {
	out := map[string]string{}
	for name, r := range c.file.GetModels().GetRoles() {
		out[name] = r.GetAdapter()
	}
	return out
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

// project returns the project section of the file for the kernel
// (decision 0250).
func (c *config) project() kernel.Project {
	p := c.file.GetProject()
	return kernel.Project{
		Commands: p.GetCheckCommands(), Conventions: p.GetConventionFiles(), ProtectedPaths: p.GetProtectedPaths(),
	}
}

// checkProject returns the errors of the project section p of the file at
// path (decision 0250): an empty command, and a convention file that is
// not a clean path inside the repository, with forward slashes.
func checkProject(path string, p *configv1.Project) []error {
	var errs []error
	for i, c := range p.GetCheckCommands() {
		if strings.TrimSpace(c) == "" {
			errs = append(errs, fmt.Errorf("the configuration file %s: project.check_commands[%d] is empty", path, i))
		}
	}
	for i, f := range p.GetConventionFiles() {
		if f == "" || !fs.ValidPath(f) || f == "." || strings.Contains(f, `\`) {
			errs = append(errs, fmt.Errorf("the configuration file %s: project.convention_files[%d] %q is not a clean path inside the repository, relative to its root and with forward slashes", path, i, f))
		}
	}
	return errs
}
