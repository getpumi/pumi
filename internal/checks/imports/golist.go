// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package imports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// pkg is one package as go list -json reports it.
type pkg struct {
	ImportPath string
	Name       string
	ForTest    string
	Standard   bool
	Module     *struct {
		Path string
		Main bool
	}
	Imports []string
	Deps    []string
	Error   *struct {
		Err string
	}
}

// list runs go list -e -deps -test -json ./... in dir, the root of a module,
// and returns every package it reports: the module's packages, their test
// variants and test main packages, and every package they depend on. -e
// reports a package that cannot be loaded as a package with an Error instead
// of stopping, so the caller decides which errors matter.
//
// go.work is turned off, so the module's own go.mod decides how each import
// resolves, as it does in CI. The command has constant arguments (G204).
func list(ctx context.Context, dir string) ([]pkg, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-deps", "-test", "-json", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list in %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	var pkgs []pkg
	dec := json.NewDecoder(&stdout)
	for {
		var p pkg
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return pkgs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("go list in %s: %w", dir, err)
		}
		pkgs = append(pkgs, p)
	}
}

// base removes the suffix that go list adds to a package compiled for a
// test, as in "example.com/a [example.com/b.test]".
func base(importPath string) string {
	p, _, _ := strings.Cut(importPath, " [")
	return p
}

// inModule reports whether p is a package of the module in which go list ran,
// leaving out the test main packages that go list generates, which import
// the package under test and nothing of the module's own choice.
func (p *pkg) inModule() bool {
	if p.Module == nil || !p.Module.Main {
		return false
	}
	testMain := !strings.Contains(p.ImportPath, " [") && strings.HasSuffix(p.ImportPath, ".test")
	return !testMain
}

// owner returns the import path of the package that p belongs to: the
// package itself, or for a test variant or an external test package, the
// package under test.
func (p *pkg) owner() string {
	if p.ForTest != "" {
		return p.ForTest
	}
	return base(p.ImportPath)
}

// variant reports whether p is a test variant or an external test package.
func (p *pkg) variant() bool {
	return strings.Contains(p.ImportPath, " [")
}

// mainModule returns the path of the module in which go list ran.
func mainModule(pkgs []pkg) (string, error) {
	for i := range pkgs {
		if pkgs[i].inModule() {
			return pkgs[i].Module.Path, nil
		}
	}
	return "", errors.New("go list reported no package of the module")
}
