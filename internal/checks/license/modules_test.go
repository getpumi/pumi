// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// writeFiles writes files, by their slash-separated path under dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// addModule adds the module path at version v1.0.0, with files, to the
// module proxy in the directory proxy, in the layout of a GOPROXY file URL.
func addModule(t *testing.T, proxy, path string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(proxy, filepath.FromSlash(path), "@v")
	gomod := "module " + path + "\n\ngo 1.24\n"
	writeFiles(t, dir, map[string]string{
		"list":        "v1.0.0\n",
		"v1.0.0.info": `{"Version": "v1.0.0", "Time": "2026-01-01T00:00:00Z"}`,
		"v1.0.0.mod":  gomod,
	})
	var zipped bytes.Buffer
	w := zip.NewWriter(&zipped)
	files["go.mod"] = gomod
	for name, content := range files {
		fw, err := w.Create(path + "@v1.0.0/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "v1.0.0.zip"), zipped.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// testGoEnv points the go command at a module proxy in a temporary directory and
// at an empty module cache, and returns the proxy directory and GOPATH.
func testGoEnv(t *testing.T) (proxy, gopath string) {
	t.Helper()
	proxy = t.TempDir()
	gopath = t.TempDir()
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOPATH", gopath)
	t.Setenv("GOMODCACHE", filepath.Join(gopath, "pkg", "mod"))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "-mod=mod -modcacherw")
	t.Setenv("GOTOOLCHAIN", "local")
	return proxy, gopath
}

func pkg(path string) map[string]string {
	name := path[strings.LastIndex(path, "/")+1:]
	return map[string]string{"p.go": "package " + name + "\n", "LICENSE": "license of " + path + "\n"}
}

// newRepo writes a core module with one binary to a temporary directory. The
// binary imports example.com/all on every platform and example.com/win only
// on Windows, example.com/cgo only with cgo and example.com/nocgo only
// without; a test imports example.com/testonly; example.com/old is replaced by
// example.com/new; and the module's own api module is replaced by ./api.
func newRepo(t *testing.T, proxy string, extra map[string]string) string {
	t.Helper()
	for _, m := range []string{"example.com/all", "example.com/win", "example.com/testonly", "example.com/new", "example.com/cgo", "example.com/nocgo"} {
		addModule(t, proxy, m, pkg(m))
	}
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/core\n\ngo 1.24\n\n" +
			"require (\n\texample.com/all v1.0.0\n\texample.com/win v1.0.0\n\texample.com/testonly v1.0.0\n\texample.com/old v1.0.0\n\texample.com/cgo v1.0.0\n\texample.com/nocgo v1.0.0\n\texample.com/core/api v0.0.0\n)\n\n" +
			"replace example.com/old => example.com/new v1.0.0\n\nreplace example.com/core/api => ./api\n",
		"api/go.mod":               "module example.com/core/api\n\ngo 1.24\n",
		"api/api.go":               "package api\n",
		"cmd/core/main.go":         "package main\n\nimport (\n\t_ \"example.com/all\"\n\t_ \"example.com/core/api\"\n\t_ \"example.com/old\"\n)\n\nfunc main() {}\n",
		"cmd/core/main_windows.go": "package main\n\nimport _ \"example.com/win\"\n",
		"cmd/core/main_test.go":    "package main\n\nimport _ \"example.com/testonly\"\n",
		"cmd/core/main_cgo.go":     "//go:build cgo\n\npackage main\n\nimport _ \"example.com/cgo\"\n",
		"cmd/core/main_nocgo.go":   "//go:build !cgo\n\npackage main\n\nimport _ \"example.com/nocgo\"\n",
	}
	for k, v := range extra {
		files[k] = v
	}
	writeFiles(t, root, files)
	return root
}

func TestBuildModules(t *testing.T) {
	proxy, gopath := testGoEnv(t)
	// The check sets cgo on and off itself, whatever the environment says.
	t.Setenv("CGO_ENABLED", "0")
	root := newRepo(t, proxy, nil)
	mods, err := BuildModules(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range mods {
		got = append(got, m.Path+" "+m.Version)
		want := filepath.Join(gopath, "pkg", "mod", filepath.FromSlash(m.Path)+"@"+m.Version)
		if m.Dir != want {
			t.Errorf("%s: Dir = %q, want %q", m.Path, m.Dir, want)
		}
	}
	want := []string{"example.com/all v1.0.0", "example.com/cgo v1.0.0", "example.com/new v1.0.0", "example.com/nocgo v1.0.0", "example.com/win v1.0.0"}
	if !slices.Equal(got, want) {
		t.Errorf("BuildModules() = %q, want %q", got, want)
	}
}

func TestBuildModulesRejectsAReplaceOutsideTheRepository(t *testing.T) {
	proxy, _ := testGoEnv(t)
	outside := t.TempDir()
	writeFiles(t, outside, map[string]string{"go.mod": "module example.com/far\n\ngo 1.24\n", "far.go": "package far\n"})
	root := newRepo(t, proxy, nil)
	writeFiles(t, root, map[string]string{
		"go.mod":                   "module example.com/core\n\ngo 1.24\n\nrequire example.com/far v0.0.0\n\nreplace example.com/far => " + filepath.ToSlash(outside) + "\n",
		"cmd/core/main.go":         "package main\n\nimport _ \"example.com/far\"\n\nfunc main() {}\n",
		"cmd/core/main_windows.go": "package main\n",
		"cmd/core/main_test.go":    "package main\n",
		"cmd/core/main_cgo.go":     "package main\n",
		"cmd/core/main_nocgo.go":   "package main\n",
	})
	_, err := BuildModules(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "example.com/far is replaced by a directory outside the repository") {
		t.Errorf("BuildModules() error = %v, want one about the replace", err)
	}
}

func TestBuildModulesRejectsAnotherModuleReplacedInsideTheRepository(t *testing.T) {
	proxy, _ := testGoEnv(t)
	root := newRepo(t, proxy, nil)
	writeFiles(t, root, map[string]string{
		"go.mod":                   "module example.com/core\n\ngo 1.24\n\nrequire example.com/fork v0.0.0\n\nreplace example.com/fork => ./third_party/fork\n",
		"third_party/fork/go.mod":  "module example.com/fork\n\ngo 1.24\n",
		"third_party/fork/fork.go": "package fork\n",
		"cmd/core/main.go":         "package main\n\nimport _ \"example.com/fork\"\n\nfunc main() {}\n",
		"cmd/core/main_windows.go": "package main\n",
		"cmd/core/main_test.go":    "package main\n",
		"cmd/core/main_cgo.go":     "package main\n",
		"cmd/core/main_nocgo.go":   "package main\n",
	})
	_, err := BuildModules(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "example.com/fork is replaced by a directory but is not a module of the repository") {
		t.Errorf("BuildModules() error = %v, want one about the replace", err)
	}
}

func TestBuildModulesReportsAGoListError(t *testing.T) {
	proxy, _ := testGoEnv(t)
	root := newRepo(t, proxy, map[string]string{"cmd/core/main.go": "package main\n\nimport _ \"example.com/missing\"\n\nfunc main() {}\n"})
	if _, err := BuildModules(context.Background(), root); err == nil {
		t.Error("BuildModules() error = nil, want one for the missing module")
	}
}

func TestPlatforms(t *testing.T) {
	got, err := Platforms(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"linux/amd64", "linux/arm64", "darwin/arm64", "windows/amd64", runtime.GOOS + "/" + runtime.GOARCH} {
		if !slices.Contains(got, p) {
			t.Errorf("Platforms() = %v, want it to contain %s", got, p)
		}
	}
	for _, p := range []string{"js/wasm", "plan9/amd64"} {
		if slices.Contains(got, p) {
			t.Errorf("Platforms() = %v, want no %s, which is not a first-class port", got, p)
		}
	}
}

func TestStandardLibrary(t *testing.T) {
	m, err := StandardLibrary(context.Background(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if m.Path != StdPath || m.Version != runtime.Version() {
		t.Errorf("StandardLibrary() = %+v, want std %s", m, runtime.Version())
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "LICENSE")); err != nil {
		t.Errorf("StandardLibrary() Dir %q has no LICENSE: %v", m.Dir, err)
	}
}
