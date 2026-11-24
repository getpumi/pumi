// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package blobstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
)

// hashPrefix starts every blob hash: the blob's SHA-256, as the hashes of
// the event log are written.
const hashPrefix = "sha256:"

// blobsName is the directory in the pumi directory that holds the blobs,
// one directory for each tenant (decision 0232).
const blobsName = "blobs"

// tempPrefix starts the name of a temporary file. A name with it is never
// a hash, so a temporary file that a crash leaves is never read as a blob.
const tempPrefix = ".tmp-"

// The errors of Get.
var (
	// ErrNotFound is the error of Get for a blob that the store does not
	// hold for the tenant.
	ErrNotFound = errors.New("blobstore: no such blob")
	// ErrDamaged is the error of Get for a blob whose content does not
	// match its hash.
	ErrDamaged = errors.New("blobstore: the blob's content does not match its hash")
)

// errCrashed is the error of Put when a test's crash hook stops it.
var errCrashed = errors.New("blobstore: stopped by the crash hook")

// Store is the blob store port (decision 0033): content stored by its
// SHA-256 hash, under a tenant, with no sharing of identical content
// across tenants (decision 0039). The local implementation is Local;
// remote mode adds one for S3-compatible stores.
type Store interface {
	// Put stores the content read from content under tenant and returns
	// its hash, "sha256:" and the hexadecimal SHA-256. Storing the same
	// content again returns the same hash.
	Put(ctx context.Context, tenant string, content io.Reader) (string, error)
	// Get returns the content of the blob hash of tenant. It fails with
	// ErrNotFound when the store does not hold it, and with ErrDamaged
	// when the stored content does not match the hash.
	Get(ctx context.Context, tenant, hash string) ([]byte, error)
}

// Local is the local blob store of embedded mode. It keeps each blob in
// the file blobs/<tenant>/<sha256> of the pumi directory, the hexadecimal
// SHA-256 of its content (decision 0232). Its methods are safe for
// concurrent use.
type Local struct {
	// root confines the store's file access to the pumi directory.
	root *os.Root

	mu     sync.RWMutex
	closed bool

	// crash, when set, is called before each step of Put, and Put stops
	// there as a crash would when it returns true. Tests use it.
	crash func(step string) bool
}

// Local is a Store.
var _ Store = (*Local)(nil)

// OpenLocal opens the local blob store in the pumi directory dir, such as
// the directory of the embedded log. The directory must exist.
//
// OpenLocal removes the temporary files that a Put stopped by a crash left
// in each tenant's directory, and no other file. The caller must hold the
// lock of the embedded log in dir, as pumi run does, so that no other
// process can be writing one of them.
func OpenLocal(dir string) (*Local, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("blobstore: opening %s: %w", dir, err)
	}
	if err := removeTemporaryFiles(root); err != nil {
		_ = root.Close()
		return nil, err
	}
	return &Local{root: root}, nil
}

// removeTemporaryFiles removes the temporary files from the directory of
// each tenant in blobs.
func removeTemporaryFiles(root *os.Root) error {
	tenants, err := readDir(root, blobsName)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("blobstore: reading %s: %w", blobsName, err)
	}
	for _, tenant := range tenants {
		if tenant.IsDir() && checkTenant(tenant.Name()) == nil {
			if err := removeTemporaryFilesIn(root, path.Join(blobsName, tenant.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// removeTemporaryFilesIn removes each regular file whose name starts with
// tempPrefix from the directory dir of root, and syncs dir when it removed
// one.
func removeTemporaryFilesIn(root *os.Root, dir string) error {
	files, err := readDir(root, dir)
	if err != nil {
		return fmt.Errorf("blobstore: reading %s: %w", dir, err)
	}
	removed := false
	for _, f := range files {
		if !f.Type().IsRegular() || !strings.HasPrefix(f.Name(), tempPrefix) {
			continue
		}
		if err := root.Remove(path.Join(dir, f.Name())); err != nil {
			return fmt.Errorf("blobstore: removing the temporary file %s: %w", path.Join(dir, f.Name()), err)
		}
		removed = true
	}
	if !removed {
		return nil
	}
	if err := syncDir(root, dir); err != nil {
		return fmt.Errorf("blobstore: syncing %s: %w", dir, err)
	}
	return nil
}

// readDir returns the entries of the directory dir of root.
func readDir(root *os.Root, dir string) ([]fs.DirEntry, error) {
	d, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	return d.ReadDir(-1)
}

// Close closes the store. A closed store refuses every call.
func (l *Local) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.root.Close()
}

// Put stores content as a blob of tenant. The blob is either whole or
// absent after a crash: Put writes the content to a temporary file in the
// tenant's directory and syncs it, renames it to the hash, and syncs the
// directory. A blob that the store already holds is written again, which
// replaces a damaged copy.
func (l *Local) Put(ctx context.Context, tenant string, content io.Reader) (string, error) {
	if err := checkTenant(tenant); err != nil {
		return "", err
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return "", errors.New("blobstore: the store is closed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	dir := path.Join(blobsName, tenant)
	if err := l.root.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("blobstore: creating the directory of the tenant %q: %w", tenant, err)
	}
	temp, f, err := l.createTemp(dir)
	if err != nil {
		return "", err
	}
	sum, err := l.write(f, content)
	if errors.Is(err, errCrashed) {
		return "", err
	}
	if err != nil {
		_ = l.root.Remove(temp)
		return "", err
	}
	name := hex.EncodeToString(sum)
	if l.stop("rename") {
		return "", errCrashed
	}
	if err := l.root.Rename(temp, path.Join(dir, name)); err != nil {
		_ = l.root.Remove(temp)
		return "", fmt.Errorf("blobstore: storing the blob %s: %w", name, err)
	}
	if l.stop("sync directory") {
		return "", errCrashed
	}
	// The rename survives a crash once the directory is synced, and the
	// tenant's directory once its parents are.
	for _, d := range []string{dir, blobsName, "."} {
		if err := syncDir(l.root, d); err != nil {
			return "", fmt.Errorf("blobstore: syncing %s: %w", d, err)
		}
	}
	return hashPrefix + name, nil
}

// createTemp creates a new temporary file in dir and returns its name and
// the open file.
func (l *Local) createTemp(dir string) (string, *os.File, error) {
	for range 10 {
		temp := path.Join(dir, tempPrefix+rand.Text())
		f, err := l.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("blobstore: creating a temporary file: %w", err)
		}
		return temp, f, nil
	}
	return "", nil, errors.New("blobstore: no free name for a temporary file")
}

// write copies content to f, syncs and closes f, and returns the SHA-256
// of the content.
func (l *Local) write(f *os.File, content io.Reader) ([]byte, error) {
	if l.stop("write") {
		_ = f.Close()
		return nil, errCrashed
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), content); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("blobstore: writing the blob: %w", err)
	}
	if l.stop("sync") {
		_ = f.Close()
		return nil, errCrashed
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("blobstore: syncing the blob: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("blobstore: writing the blob: %w", err)
	}
	return h.Sum(nil), nil
}

// stop reports whether the crash hook stops Put before step.
func (l *Local) stop(step string) bool {
	return l.crash != nil && l.crash(step)
}

// Get returns the content of the blob hash of tenant, after it checks
// that the content matches the hash.
func (l *Local) Get(ctx context.Context, tenant, hash string) ([]byte, error) {
	if err := checkTenant(tenant); err != nil {
		return nil, err
	}
	name, err := checkHash(hash)
	if err != nil {
		return nil, err
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return nil, errors.New("blobstore: the store is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	content, err := l.root.ReadFile(path.Join(blobsName, tenant, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s of the tenant %q", ErrNotFound, hash, tenant)
	}
	if err != nil {
		return nil, fmt.Errorf("blobstore: reading the blob %s: %w", hash, err)
	}
	if sum := sha256.Sum256(content); hex.EncodeToString(sum[:]) != name {
		return nil, fmt.Errorf("%w: %s of the tenant %q", ErrDamaged, hash, tenant)
	}
	return content, nil
}

// checkTenant refuses a tenant that is not one path segment of letters,
// digits, '-', '_' and '.', or that starts with '.', so that it always
// names a directory of its own in blobs.
func checkTenant(tenant string) error {
	if tenant == "" || strings.HasPrefix(tenant, ".") {
		return fmt.Errorf("blobstore: the tenant %q is empty or starts with '.'", tenant)
	}
	for _, c := range tenant {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			return fmt.Errorf("blobstore: the tenant %q has the character %q", tenant, c)
		}
	}
	return nil
}

// checkHash returns the file name of hash, its hexadecimal SHA-256, and
// refuses a hash that is not "sha256:" and 64 lowercase hexadecimal
// digits.
func checkHash(hash string) (string, error) {
	name, ok := strings.CutPrefix(hash, hashPrefix)
	if !ok || len(name) != 2*sha256.Size {
		return "", fmt.Errorf("blobstore: %q is not a blob hash", hash)
	}
	for _, c := range name {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("blobstore: %q is not a blob hash", hash)
		}
	}
	return name, nil
}

// Tenant is a Store bound to one tenant, for a caller, such as a runner,
// that takes its tenant from its identity and never names one.
type Tenant struct {
	store  Store
	tenant string
}

// ForTenant returns store bound to tenant.
func ForTenant(store Store, tenant string) Tenant {
	return Tenant{store: store, tenant: tenant}
}

// Put stores content as a blob of the tenant and returns its hash.
func (t Tenant) Put(ctx context.Context, content io.Reader) (string, error) {
	return t.store.Put(ctx, t.tenant, content)
}

// Get returns the content of the blob hash of the tenant.
func (t Tenant) Get(ctx context.Context, hash string) ([]byte, error) {
	return t.store.Get(ctx, t.tenant, hash)
}
