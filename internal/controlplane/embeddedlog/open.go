// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package embeddedlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"google.golang.org/protobuf/encoding/protojson"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
)

// head is the content of the head file: the position of the last committed
// event and the hash of its line. An empty log has position 0 and no hash.
type head struct {
	Position uint64 `json:"position"`
	Hash     string `json:"hash,omitempty"`
}

// hashForm is the form of a line's hash.
var hashForm = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// load reads the head and the log into l, creating both for a new log, and
// drops the lines after the committed head.
func (l *Log) load() error {
	h, err := l.readHead()
	if err != nil {
		return err
	}
	logPath := filepath.Join(l.dir, logName)
	flag := os.O_RDWR
	if h.Position == 0 {
		// Only an empty log may be created; a missing log with committed
		// events is not repaired.
		flag |= os.O_CREATE
	}
	l.file, err = l.root.OpenFile(logName, flag, 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s line 1: missing, while head commits position %d", logPath, h.Position)
	}
	if err != nil {
		return fmt.Errorf("opening %s: %w", logPath, err)
	}
	if err := l.readLines(h); err != nil {
		return err
	}
	info, err := l.file.Stat()
	if err != nil {
		return fmt.Errorf("reading the size of %s: %w", logPath, err)
	}
	if info.Size() > l.end {
		// The append that wrote these lines never replaced head, so it
		// never returned.
		if err := l.file.Truncate(l.end); err != nil {
			return fmt.Errorf("dropping the lines after the committed head of %s: %w", logPath, err)
		}
		if err := l.file.Sync(); err != nil {
			return fmt.Errorf("syncing %s: %w", logPath, err)
		}
	}
	return nil
}

// readHead reads the head file. When neither it nor log.jsonl exists, the
// log is new, and readHead writes a head at position 0.
func (l *Log) readHead() (head, error) {
	headPath := filepath.Join(l.dir, headName)
	b, err := l.root.ReadFile(headName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if _, err := l.root.Stat(logName); !errors.Is(err, fs.ErrNotExist) {
			return head{}, fmt.Errorf("%s: missing while %s exists; the log is not repaired automatically", headPath, logName)
		}
		return head{}, l.replaceHead(head{})
	case err != nil:
		return head{}, fmt.Errorf("%s: %w", headPath, err)
	}
	var h head
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&h); err != nil {
		return head{}, fmt.Errorf("%s: not the JSON of a head: %w", headPath, err)
	}
	if (h.Position == 0) != (h.Hash == "") || (h.Hash != "" && !hashForm.MatchString(h.Hash)) {
		return head{}, fmt.Errorf("%s: position %d with the hash %q", headPath, h.Position, h.Hash)
	}
	return h, nil
}

// readLines reads the committed lines up to h into l and checks each one
// and the head.
func (l *Log) readLines(h head) error {
	logPath := filepath.Join(l.dir, logName)
	r := bufio.NewReader(l.file)
	for p := uint64(1); p <= h.Position; p++ {
		line, err := r.ReadBytes('\n')
		switch {
		case errors.Is(err, io.EOF) && len(line) == 0:
			return fmt.Errorf("%s line %d: missing, while head commits position %d", logPath, p, h.Position)
		case errors.Is(err, io.EOF):
			return fmt.Errorf("%s line %d: no newline at its end, while head commits it", logPath, p)
		case err != nil:
			return fmt.Errorf("%s line %d: %w", logPath, p, err)
		}
		line = line[:len(line)-1]
		rec, err := l.check(line)
		if err != nil {
			return fmt.Errorf("%s line %d: %w", logPath, p, err)
		}
		rec.offset = l.end
		l.add(rec)
		l.hash = hashOf(line)
		l.end += int64(len(line)) + 1
	}
	if l.hash != h.Hash {
		return fmt.Errorf("%s line %d: its hash is %s, while head commits %s", logPath, h.Position, l.hash, h.Hash)
	}
	return nil
}

// check checks that line is the protojson of a Pumi event of the tenant
// Tenant whose prevhash is the hash of the line before it, and returns its
// record without its offset.
func (l *Log) check(line []byte) (record, error) {
	var e cloudeventsv1.CloudEvent
	if err := protojson.Unmarshal(line, &e); err != nil {
		return record{}, fmt.Errorf("not the protojson of a CloudEvent: %w", err)
	}
	if err := events.Validate(&e); err != nil {
		return record{}, fmt.Errorf("not a Pumi event: %w", err)
	}
	attrs := e.GetAttributes()
	if got := attrs[events.ExtensionTenant].GetCeString(); got != Tenant {
		return record{}, fmt.Errorf("the tenant is %q, want %q", got, Tenant)
	}
	got := attrs[events.ExtensionPrevHash].GetCeString()
	switch {
	case l.hash == "" && got != "":
		return record{}, fmt.Errorf("the first event has the prevhash %s, want none", got)
	case got != l.hash:
		return record{}, fmt.Errorf("the prevhash is %q, want %s, the hash of the line before it", got, l.hash)
	}
	return record{length: len(line), typ: e.GetType(), subject: attrs["subject"].GetCeString()}, nil
}

// replaceHead replaces the head file with h: it writes and syncs head.tmp,
// renames it to head and syncs the directory.
func (l *Log) replaceHead(h head) error {
	if err := l.writeTemp(h); err != nil {
		return err
	}
	l.step("rename head.tmp to head")
	if err := l.root.Rename(tempName, headName); err != nil {
		return fmt.Errorf("replacing %s: %w", filepath.Join(l.dir, headName), err)
	}
	l.step("sync directory")
	if err := syncDir(l.root); err != nil {
		return fmt.Errorf("syncing %s: %w", l.dir, err)
	}
	return nil
}

// writeTemp writes h to head.tmp and syncs it.
func (l *Log) writeTemp(h head) error {
	path := filepath.Join(l.dir, tempName)
	b, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("encoding the head: %w", err)
	}
	l.step("write head.tmp")
	f, err := l.root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	l.step("sync head.tmp")
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("syncing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	return nil
}
