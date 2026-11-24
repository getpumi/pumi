// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package blobstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hashOf returns the blob hash of content.
func hashOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// newLocal returns a local store in a new pumi directory, closed when the
// test ends, and the directory.
func newLocal(t *testing.T) (*Local, string) {
	t.Helper()
	pumi := t.TempDir()
	l, err := OpenLocal(pumi)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, pumi
}

// entries returns the names in the directory dir.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteAndRead(t *testing.T) {
	l, pumi := newLocal(t)
	hash, err := l.Put(t.Context(), "local", strings.NewReader("report\n"))
	if err != nil {
		t.Fatal(err)
	}
	if hash != hashOf("report\n") {
		t.Errorf("Put = %s, want %s", hash, hashOf("report\n"))
	}
	hex := strings.TrimPrefix(hash, "sha256:")
	if got := entries(t, filepath.Join(pumi, "blobs", "local")); len(got) != 1 || got[0] != hex {
		t.Errorf("blobs/local holds %q, want only %s (decision 0232)", got, hex)
	}
	content, err := l.Get(t.Context(), "local", hash)
	if err != nil || string(content) != "report\n" {
		t.Errorf("Get = %q, %v; want the content", content, err)
	}
}

func TestAnEmptyBlob(t *testing.T) {
	l, _ := newLocal(t)
	hash, err := l.Put(t.Context(), "local", strings.NewReader(""))
	if err != nil || hash != hashOf("") {
		t.Fatalf("Put = %s, %v; want %s", hash, err, hashOf(""))
	}
	if content, err := l.Get(t.Context(), "local", hash); err != nil || len(content) != 0 {
		t.Errorf("Get = %q, %v; want no content", content, err)
	}
}

func TestTheSameBlobTwice(t *testing.T) {
	l, pumi := newLocal(t)
	first, err := l.Put(t.Context(), "local", strings.NewReader("same"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.Put(t.Context(), "local", strings.NewReader("same"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("the hashes are %s and %s, want the same", first, second)
	}
	if got := entries(t, filepath.Join(pumi, "blobs", "local")); len(got) != 1 {
		t.Errorf("blobs/local holds %q, want one blob", got)
	}
	if content, err := l.Get(t.Context(), "local", first); err != nil || string(content) != "same" {
		t.Errorf("Get = %q, %v; want the content", content, err)
	}
}

func TestAMissingBlob(t *testing.T) {
	l, _ := newLocal(t)
	content, err := l.Get(t.Context(), "local", hashOf("never written"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %q, %v; want ErrNotFound", content, err)
	}
}

func TestADamagedBlobIsRefused(t *testing.T) {
	l, pumi := newLocal(t)
	hash, err := l.Put(t.Context(), "local", strings.NewReader("report\n"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(pumi)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	name := filepath.Join("blobs", "local", strings.TrimPrefix(hash, "sha256:"))
	if err := root.WriteFile(name, []byte("damaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := l.Get(t.Context(), "local", hash)
	if !errors.Is(err, ErrDamaged) {
		t.Errorf("Get = %q, %v; want ErrDamaged", content, err)
	}
	if content != nil {
		t.Errorf("Get returned the damaged content %q", content)
	}
}

func TestWritingTheSameBlobAgainReplacesADamagedOne(t *testing.T) {
	l, pumi := newLocal(t)
	hash, err := l.Put(t.Context(), "local", strings.NewReader("report\n"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(pumi)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.WriteFile(filepath.Join("blobs", "local", strings.TrimPrefix(hash, "sha256:")), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Put(t.Context(), "local", strings.NewReader("report\n")); err != nil {
		t.Fatal(err)
	}
	if content, err := l.Get(t.Context(), "local", hash); err != nil || string(content) != "report\n" {
		t.Errorf("Get = %q, %v; want the content", content, err)
	}
}

func TestTwoTenants(t *testing.T) {
	l, pumi := newLocal(t)
	a, err := l.Put(t.Context(), "tenant-a", strings.NewReader("shared content"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Get(t.Context(), "tenant-b", a); !errors.Is(err, ErrNotFound) {
		t.Errorf("tenant-b reads tenant-a's blob: %v; want ErrNotFound (decision 0039)", err)
	}
	b, err := l.Put(t.Context(), "tenant-b", strings.NewReader("shared content"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("the hashes are %s and %s, want the same", a, b)
	}
	// Identical content is stored once for each tenant, never shared.
	hex := strings.TrimPrefix(a, "sha256:")
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		if got := entries(t, filepath.Join(pumi, "blobs", tenant)); len(got) != 1 || got[0] != hex {
			t.Errorf("blobs/%s holds %q, want only %s", tenant, got, hex)
		}
	}
}

func TestABadTenantIsRefused(t *testing.T) {
	l, pumi := newLocal(t)
	for _, tenant := range []string{"", ".", "..", "a/b", `a\b`, ".hidden", "a b", "../escape"} {
		if _, err := l.Put(t.Context(), tenant, strings.NewReader("x")); err == nil {
			t.Errorf("Put with the tenant %q = nil; want an error", tenant)
		}
		if _, err := l.Get(t.Context(), tenant, hashOf("x")); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Get with the tenant %q = %v; want an error other than ErrNotFound", tenant, err)
		}
	}
	if _, err := os.Stat(filepath.Join(pumi, "blobs")); err == nil {
		if got := entries(t, filepath.Join(pumi, "blobs")); len(got) != 0 {
			t.Errorf("blobs holds %q after refused writes", got)
		}
	}
}

func TestABadHashIsRefused(t *testing.T) {
	l, _ := newLocal(t)
	good := hashOf("x")
	for _, hash := range []string{
		"", strings.TrimPrefix(good, "sha256:"), "sha1:" + strings.TrimPrefix(good, "sha256:"),
		strings.ToUpper(good[:7]) + good[7:], good[:len(good)-1], good + "0",
		"sha256:" + strings.Repeat("g", 64), "sha256:../../" + strings.Repeat("0", 58),
	} {
		if _, err := l.Get(t.Context(), "local", hash); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) = %v; want an error other than ErrNotFound", hash, err)
		}
	}
}

// failingReader returns its content and then an error.
type failingReader struct{ r *strings.Reader }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.r.Len() == 0 {
		return 0, errors.New("the read failed")
	}
	return f.r.Read(p)
}

func TestAFailedWriteLeavesNoBlob(t *testing.T) {
	l, pumi := newLocal(t)
	if _, err := l.Put(t.Context(), "local", &failingReader{strings.NewReader("part of it")}); err == nil {
		t.Fatal("Put = nil; want the read's error")
	}
	if got := entries(t, filepath.Join(pumi, "blobs", "local")); len(got) != 0 {
		t.Errorf("blobs/local holds %q after a failed write, want nothing", got)
	}
	if _, err := l.Get(t.Context(), "local", hashOf("part of it")); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v; want ErrNotFound", err)
	}
}

func TestACrashBeforeTheRenameLeavesNoBlob(t *testing.T) {
	// The process stops after the content is written and synced to the
	// temporary file and before the rename: the blob is absent, and the
	// temporary file, whose name is not a hash, is never read as one.
	l, pumi := newLocal(t)
	l.crash = func(step string) bool { return step == "rename" }
	if _, err := l.Put(t.Context(), "local", strings.NewReader("report\n")); !errors.Is(err, errCrashed) {
		t.Fatalf("Put = %v; want the crash", err)
	}
	left := entries(t, filepath.Join(pumi, "blobs", "local"))
	if len(left) != 1 || !strings.HasPrefix(left[0], tempPrefix) {
		t.Fatalf("blobs/local holds %q, want one temporary file", left)
	}
	if _, err := l.Get(t.Context(), "local", hashOf("report\n")); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get = %v; want ErrNotFound", err)
	}
	if _, err := l.Get(t.Context(), "local", "sha256:"+left[0]); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Get of the temporary file's name = %v; want a bad hash", err)
	}
	l.crash = nil
	hash, err := l.Put(t.Context(), "local", strings.NewReader("report\n"))
	if err != nil {
		t.Fatal(err)
	}
	if content, err := l.Get(t.Context(), "local", hash); err != nil || string(content) != "report\n" {
		t.Errorf("Get after the next Put = %q, %v; want the content", content, err)
	}
}

func TestPutSyncsBeforeTheRenameAndAfter(t *testing.T) {
	l, _ := newLocal(t)
	steps := []string{}
	l.crash = func(step string) bool {
		steps = append(steps, step)
		return false
	}
	if _, err := l.Put(t.Context(), "local", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	want := []string{"write", "sync", "rename", "sync directory"}
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Errorf("the steps are %q, want %q", steps, want)
	}
}

func TestForTenant(t *testing.T) {
	l, _ := newLocal(t)
	s := ForTenant(l, "local")
	hash, err := s.Put(t.Context(), bytes.NewReader([]byte("scoped")))
	if err != nil {
		t.Fatal(err)
	}
	if content, err := l.Get(t.Context(), "local", hash); err != nil || string(content) != "scoped" {
		t.Errorf("Get = %q, %v; want the content that ForTenant's Put wrote", content, err)
	}
	if content, err := s.Get(t.Context(), hash); err != nil || string(content) != "scoped" {
		t.Errorf("ForTenant's Get = %q, %v; want the content", content, err)
	}
}

func TestAClosedStoreRefusesCalls(t *testing.T) {
	l, _ := newLocal(t)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Put(t.Context(), "local", strings.NewReader("x")); err == nil {
		t.Error("Put on a closed store = nil; want an error")
	}
}

// crashedPut runs a Put of content for tenant that stops before the
// rename, as a crash would, which leaves a temporary file.
func crashedPut(t *testing.T, l *Local, tenant, content string) {
	t.Helper()
	l.crash = func(step string) bool { return step == "rename" }
	defer func() { l.crash = nil }()
	if _, err := l.Put(t.Context(), tenant, strings.NewReader(content)); !errors.Is(err, errCrashed) {
		t.Fatalf("Put = %v; want the crash", err)
	}
}

// reopen closes l and opens the store in pumi again.
func reopen(t *testing.T, l *Local, pumi string) *Local {
	t.Helper()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := OpenLocal(pumi)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	return again
}

func TestOpeningRemovesTheTemporaryFilesThatACrashLeft(t *testing.T) {
	l, pumi := newLocal(t)
	a, err := l.Put(t.Context(), "tenant-a", strings.NewReader("kept a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.Put(t.Context(), "tenant-b", strings.NewReader("kept b"))
	if err != nil {
		t.Fatal(err)
	}
	crashedPut(t, l, "tenant-a", "lost a")
	crashedPut(t, l, "tenant-b", "lost b")
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		if got := entries(t, filepath.Join(pumi, "blobs", tenant)); len(got) != 2 || !strings.HasPrefix(got[0], tempPrefix) {
			t.Fatalf("blobs/%s holds %q before the store is opened again, want a temporary file and a blob", tenant, got)
		}
	}
	// A file whose name is not a temporary file's is not the store's to
	// remove.
	root, err := os.OpenRoot(pumi)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.WriteFile(filepath.Join("blobs", "tenant-a", "notes"), []byte("someone's notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	l = reopen(t, l, pumi)
	// A hash sorts before "notes": its digits and letters a to f come first.
	wantA := []string{strings.TrimPrefix(a, "sha256:"), "notes"}
	if got := entries(t, filepath.Join(pumi, "blobs", "tenant-a")); strings.Join(got, ",") != strings.Join(wantA, ",") {
		t.Errorf("blobs/tenant-a holds %q after the store was opened again, want %q", got, wantA)
	}
	if got := entries(t, filepath.Join(pumi, "blobs", "tenant-b")); len(got) != 1 || got[0] != strings.TrimPrefix(b, "sha256:") {
		t.Errorf("blobs/tenant-b holds %q after the store was opened again, want only its blob", got)
	}
	for tenant, hash := range map[string]string{"tenant-a": a, "tenant-b": b} {
		if _, err := l.Get(t.Context(), tenant, hash); err != nil {
			t.Errorf("Get of %s's blob after the store was opened again: %v", tenant, err)
		}
	}
}

func TestOpeningLeavesAStoreWithNoTemporaryFilesUnchanged(t *testing.T) {
	l, pumi := newLocal(t)
	hash, err := l.Put(t.Context(), "local", strings.NewReader("kept"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(pumi, "blobs", "local", strings.TrimPrefix(hash, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	l = reopen(t, l, pumi)
	if got := entries(t, filepath.Join(pumi, "blobs", "local")); len(got) != 1 || got[0] != strings.TrimPrefix(hash, "sha256:") {
		t.Errorf("blobs/local holds %q, want only its blob", got)
	}
	again, err := os.Stat(filepath.Join(pumi, "blobs", "local", strings.TrimPrefix(hash, "sha256:")))
	if err != nil || !again.ModTime().Equal(info.ModTime()) {
		t.Errorf("the blob changed when the store was opened again: %v, %v", again, err)
	}
	if content, err := l.Get(t.Context(), "local", hash); err != nil || string(content) != "kept" {
		t.Errorf("Get = %q, %v; want the content", content, err)
	}
}

func TestOpeningAStoreWithNoBlobs(t *testing.T) {
	pumi := t.TempDir()
	l, err := OpenLocal(pumi)
	if err != nil {
		t.Fatalf("OpenLocal of a pumi directory with no blobs: %v", err)
	}
	_ = l.Close()
	if got := entries(t, pumi); len(got) != 0 {
		t.Errorf("the pumi directory holds %q after OpenLocal, want nothing", got)
	}
}
