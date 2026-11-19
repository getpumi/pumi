// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// StdPath is the module path that the check gives the Go standard library,
// as go list does.
const StdPath = "std"

// Module is one module that a binary includes: its path, its version and the
// directory of its source in the module cache. For a module that go.mod
// replaces with another module, these are the replacement's, which is the
// code the binary includes.
type Module struct {
	Path    string
	Version string
	Dir     string
}

// goCmd runs cmd in dir with env added to the environment and returns its
// standard output. go.work is turned off, so the module's own go.mod decides
// how each import resolves, as it does in CI. Each caller passes constant
// arguments (G204).
func goCmd(dir string, env []string, cmd *exec.Cmd) ([]byte, error) {
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "PWD="+dir, "GOWORK=off"), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s in %s %s: %w: %s", strings.Join(cmd.Args[:3], " "), dir, strings.Join(env, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Platforms returns Go's first-class ports, as GOOS/GOARCH, from the go
// command that runs in dir. The release platforms are not chosen yet, so the
// check covers every first-class port (decision 0126).
func Platforms(ctx context.Context, dir string) ([]string, error) {
	out, err := goCmd(dir, nil, exec.CommandContext(ctx, "go", "tool", "dist", "list", "-json"))
	if err != nil {
		return nil, err
	}
	var ports []struct {
		GOOS, GOARCH string
		FirstClass   bool
	}
	if err := json.Unmarshal(out, &ports); err != nil {
		return nil, fmt.Errorf("go tool dist list: %w", err)
	}
	var platforms []string
	for _, p := range ports {
		if p.FirstClass {
			platforms = append(platforms, p.GOOS+"/"+p.GOARCH)
		}
	}
	if len(platforms) == 0 {
		return nil, errors.New("go tool dist list: no first-class port")
	}
	return platforms, nil
}

// goEnv runs cmd, a go env -json command, in dir and returns the variables it
// prints.
func goEnv(ctx context.Context, dir string, cmd func(context.Context) *exec.Cmd) (map[string]string, error) {
	out, err := goCmd(dir, nil, cmd(ctx))
	if err != nil {
		return nil, err
	}
	var env map[string]string
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("go env: %w", err)
	}
	return env, nil
}

// StandardLibrary returns the Go standard library of the toolchain that
// builds the module in dir, with the toolchain's version and GOROOT. It is
// in every binary, so the check checks it like a module (decision 0127).
func StandardLibrary(ctx context.Context, dir string) (Module, error) {
	env, err := goEnv(ctx, dir, func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "go", "env", "-json", "GOVERSION", "GOROOT")
	})
	if err != nil {
		return Module{}, err
	}
	if env["GOVERSION"] == "" || env["GOROOT"] == "" {
		return Module{}, fmt.Errorf("go env: GOVERSION %q, GOROOT %q", env["GOVERSION"], env["GOROOT"])
	}
	return Module{Path: StdPath, Version: env["GOVERSION"], Dir: env["GOROOT"]}, nil
}

// listedModule is a module as go list -json reports it.
type listedModule struct {
	Path    string
	Version string
	Main    bool
	Dir     string
	Replace *struct {
		Path    string
		Version string
	}
}

// BuildModules returns the third-party modules that the binaries under cmd
// in the core module at root include on any of Go's first-class ports, with
// cgo on and with cgo off, sorted by path. Test files are not part of a
// binary, so a module that only tests import is not included (decision
// 0076). The core module and a module of the repository that go.mod replaces
// with a directory inside root, such as the api module, are the project's own
// and are not included. Any other module replaced with a directory fails,
// because the check cannot know its license.
func BuildModules(ctx context.Context, root string) ([]Module, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// The go command reports directories with symbolic links resolved.
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return nil, err
	}
	platforms, err := Platforms(ctx, root)
	if err != nil {
		return nil, err
	}
	seen := map[string]Module{}
	for _, p := range platforms {
		goos, goarch, _ := strings.Cut(p, "/")
		for _, cgo := range []string{"0", "1"} {
			if err := addModules(ctx, root, []string{"GOOS=" + goos, "GOARCH=" + goarch, "CGO_ENABLED=" + cgo}, seen); err != nil {
				return nil, err
			}
		}
	}
	mods := make([]Module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	slices.SortFunc(mods, func(a, b Module) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Version, b.Version))
	})
	return mods, nil
}

// addModules adds to seen, by path@version, the third-party modules that the
// binaries include in the build that env sets.
func addModules(ctx context.Context, root string, env []string, seen map[string]Module) error {
	mods, err := listModules(ctx, root, env)
	if err != nil {
		return err
	}
	mainPath := ""
	for _, m := range mods {
		if m.Main {
			mainPath = m.Path
		}
	}
	for _, m := range mods {
		mod, own, err := thirdParty(root, mainPath, m)
		if err != nil {
			return err
		}
		if !own {
			seen[mod.Path+"@"+mod.Version] = mod
		}
	}
	return nil
}

// listModules returns the modules of the packages that the binaries under cmd
// depend on in the build that env sets. -buildvcs=false stops go list from
// reading the git status of the main packages, which fails where git cannot
// read the repository.
func listModules(ctx context.Context, root string, env []string) ([]listedModule, error) {
	out, err := goCmd(root, env,
		exec.CommandContext(ctx, "go", "list", "-deps", "-buildvcs=false", "-json=ImportPath,Standard,Module", "./cmd/..."))
	if err != nil {
		return nil, err
	}
	var mods []listedModule
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p struct {
			ImportPath string
			Standard   bool
			Module     *listedModule
		}
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return mods, nil
		}
		if err != nil {
			return nil, fmt.Errorf("go list: %w", err)
		}
		switch {
		case p.Standard:
		case p.Module == nil:
			return nil, fmt.Errorf("go list: the package %s is in no module", p.ImportPath)
		default:
			mods = append(mods, *p.Module)
		}
	}
}

// thirdParty returns m as a Module, or own as true when m is the project's
// own: the main module, whose path is mainPath, or a module whose path is
// below mainPath and that go.mod replaces with a directory inside root.
func thirdParty(root, mainPath string, m listedModule) (mod Module, own bool, err error) {
	if m.Main {
		return Module{}, true, nil
	}
	if m.Replace == nil {
		return Module{Path: m.Path, Version: m.Version, Dir: m.Dir}, false, nil
	}
	if m.Replace.Version != "" {
		return Module{Path: m.Replace.Path, Version: m.Replace.Version, Dir: m.Dir}, false, nil
	}
	rel, err := filepath.Rel(root, m.Dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Module{}, false, fmt.Errorf("%s is replaced by a directory outside the repository, %s, whose license the check cannot know", m.Path, m.Dir)
	}
	if mainPath == "" || !strings.HasPrefix(m.Path, mainPath+"/") {
		return Module{}, false, fmt.Errorf("%s is replaced by a directory but is not a module of the repository, so the check cannot know its license; use a version of it", m.Path)
	}
	return Module{}, true, nil
}
