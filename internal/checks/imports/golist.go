// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package imports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// pkg is one package as go list -json reports it.
type pkg struct {
	ImportPath string
	Name       string
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

// goList runs go list with args in dir and returns its standard output. go.work
// is turned off, so the module's own go.mod decides how each import resolves,
// as it does in CI. Each caller passes constant arguments (G204).
func goList(dir string, cmd *exec.Cmd) ([]byte, error) {
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s in %s: %w: %s", strings.Join(cmd.Args[:2], " "), dir, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// decodeAll decodes a stream of JSON values, as go list -json writes them.
func decodeAll[T any](data []byte) ([]T, error) {
	var all []T
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var v T
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return all, nil
		}
		if err != nil {
			return nil, fmt.Errorf("go list: %w", err)
		}
		all = append(all, v)
	}
}

// list returns every package of the module in dir and every package they
// depend on in the build for this platform. -e reports a package that cannot
// be loaded as a package with an Error instead of stopping, so the caller
// decides which errors matter.
func list(ctx context.Context, dir string) ([]pkg, error) {
	out, err := goList(dir, exec.CommandContext(ctx, "go", "list", "-e", "-deps", "-json", "./..."))
	if err != nil {
		return nil, err
	}
	return decodeAll[pkg](out)
}

type module struct {
	Path string
	Main bool
}

// mainModule returns the path of the module that dir is in.
func mainModule(ctx context.Context, dir string) (string, error) {
	out, err := goList(dir, exec.CommandContext(ctx, "go", "list", "-m", "-json"))
	if err != nil {
		return "", err
	}
	mods, err := decodeAll[module](out)
	if err != nil {
		return "", err
	}
	if len(mods) != 1 || !mods[0].Main {
		return "", fmt.Errorf("go list -m in %s: want the main module, got %+v", dir, mods)
	}
	return mods[0].Path, nil
}

// buildList returns the paths of the modules in the build list of the module
// in dir, the main module included. go mod tidy keeps in it the modules that
// any file needs, whatever its platform or build tags, so the module of every
// import is in it.
func buildList(ctx context.Context, dir string) ([]string, error) {
	out, err := goList(dir, exec.CommandContext(ctx, "go", "list", "-m", "-json", "all"))
	if err != nil {
		return nil, err
	}
	mods, err := decodeAll[module](out)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(mods))
	for i, m := range mods {
		paths[i] = m.Path
	}
	return paths, nil
}

// inModule reports whether p is a package of the module in which go list ran.
func (p *pkg) inModule() bool {
	return p.Module != nil && p.Module.Main
}

// packageDirs returns the directories of the module rooted at root that hold
// a Go file, relative to root and slash-separated, with "." for root. It
// skips what go list ./... skips: nested modules and directories named
// testdata or vendor or starting with "." or "_". Unlike go list, it keeps a
// directory whose files build constraints all leave out on this platform.
func packageDirs(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel != "." && skipDir(path, d.Name()) {
			return filepath.SkipDir
		}
		files, err := goFiles(path)
		if len(files) > 0 {
			dirs = append(dirs, filepath.ToSlash(rel))
		}
		return err
	})
	return dirs, err
}

func skipDir(path, name string) bool {
	if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return true
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}

// goFiles returns the Go files in dir that the go command reads for some
// platform or build tag: every .go file whose name does not start with "."
// or "_", test files included.
func goFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(name, ".go") && !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_") {
			files = append(files, filepath.Join(dir, name))
		}
	}
	return files, nil
}

// dirImports returns the sorted imports of every Go file in dir, whatever its
// platform or build tags, so that an import for another platform or behind a
// build tag is checked too. Like go list, it reads only the imports.
func dirImports(dir string) ([]string, error) {
	files, err := goFiles(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			return nil, err
		}
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", fset.Position(spec.Pos()), err)
			}
			seen[path] = true
		}
	}
	imps := make([]string, 0, len(seen))
	for path := range seen {
		imps = append(imps, path)
	}
	slices.Sort(imps)
	return imps, nil
}

// standard reports whether path is in the standard library, whose import
// paths have no dot in their first element, or is cgo's pseudo-package C.
func standard(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}
