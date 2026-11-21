// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package embeddedlog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
)

// Tenant is the tenant of every event in the embedded log (decision 0157).
const Tenant = "local"

// The files in the pumi directory (decision 0157).
const (
	logName  = "log.jsonl"
	headName = "head"
	tempName = "head.tmp"
	lockName = "lock"
)

// Log is the open event log of one clone. Its methods are safe for
// concurrent use. While it is open, the process holds the lock on
// pumi/lock, and no other Log of the same clone can be opened.
type Log struct {
	dir string
	// root confines the Log's file access to dir.
	root *os.Root
	lock *os.File
	file *os.File

	mu sync.RWMutex
	// records holds each committed event, at the index of its position
	// minus one.
	records []record
	// bySubject and byType hold the positions of the events of each
	// subject and each type, in order.
	bySubject map[string][]uint64
	byType    map[string][]uint64
	// hash is the hash of the last committed line, or "" when the log is
	// empty.
	hash string
	// end is the size of the committed part of log.jsonl.
	end int64
	// broken, when set, refuses every further append, because the files
	// may no longer match what this Log holds.
	broken error
	closed bool
	// changed is closed and replaced when an append commits, and closed
	// when the Log closes, to wake the callers of Wait.
	changed chan struct{}

	// trace, when set, is called before each step of an append. Tests use
	// it to see the order of the steps.
	trace func(step string)
}

// record is what the Log keeps in memory for one committed event.
type record struct {
	offset  int64
	length  int
	typ     string
	subject string
}

// Open opens the event log of the git clone that dir is in: the files in
// pumi/ under the clone's common git directory, which every worktree of
// the clone shares. It creates them when they do not exist. It fails at
// once when another Log of the clone is open, in this process or in
// another.
//
// Open reads the whole log: it drops the lines after the committed head,
// which an append that never returned wrote, and stops with an error that
// names the line when a committed line does not parse as a Pumi event, does
// not chain to the line before it, or does not match the head. It never
// repairs the log.
func Open(ctx context.Context, dir string) (*Log, error) {
	common, err := commonDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	pumi := filepath.Join(common, "pumi")
	if err := os.MkdirAll(pumi, 0o750); err != nil {
		return nil, fmt.Errorf("creating the event log directory: %w", err)
	}
	root, err := os.OpenRoot(pumi)
	if err != nil {
		return nil, fmt.Errorf("opening the event log directory: %w", err)
	}
	lock, err := root.OpenFile(lockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("opening the event log lock: %w", err)
	}
	if err := lockFile(lock); err != nil {
		_ = lock.Close()
		_ = root.Close()
		if errors.Is(err, errLocked) {
			return nil, fmt.Errorf("the event log in %s is in use by another pumi process, or by another open of it in this one", pumi)
		}
		return nil, fmt.Errorf("locking %s: %w", filepath.Join(pumi, lockName), err)
	}
	l := &Log{
		dir: pumi, root: root, lock: lock,
		bySubject: map[string][]uint64{}, byType: map[string][]uint64{}, changed: make(chan struct{}),
	}
	if err := l.load(); err != nil {
		_ = l.release()
		return nil, err
	}
	return l, nil
}

// commonDir returns the absolute path of the common git directory of the
// clone that dir is in. The arguments of git are constant, because gosec
// reports a command built from variables (G204).
func commonDir(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("finding the git directory of %s: %w: %s", dir, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// Dir returns the pumi directory that holds the log.
func (l *Log) Dir() string {
	return l.dir
}

// Close releases the lock and closes the files. A closed Log refuses every
// call.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	close(l.changed)
	return l.release()
}

// Wait blocks until the head is after the position after, and returns nil.
// It returns the error of ctx when ctx is done first, and ErrClosed when
// the Log is closed. Subscribe waits with it for new events.
func (l *Log) Wait(ctx context.Context, after uint64) error {
	for {
		l.mu.RLock()
		closed, head, changed := l.closed, uint64(len(l.records)), l.changed
		l.mu.RUnlock()
		switch {
		case closed:
			return ErrClosed
		case head > after:
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// release closes the log file, unlocks the lock, closes it and closes the
// directory.
func (l *Log) release() error {
	var errs []error
	if l.file != nil {
		errs = append(errs, l.file.Close())
	}
	errs = append(errs, unlockFile(l.lock), l.lock.Close(), l.root.Close())
	return errors.Join(errs...)
}

// ErrClosed is the error of every call on a closed Log.
var ErrClosed = errors.New("the event log is closed")

// Head returns the position of the last committed event, or 0 when the log
// is empty.
func (l *Log) Head() uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return uint64(len(l.records))
}

// Version returns the version of subject: the number of events in the log
// with that subject (decision 0153).
func (l *Log) Version(subject string) uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.version(subject)
}

// version is Version with l.mu held.
func (l *Log) version(subject string) uint64 {
	return uint64(len(l.bySubject[subject]))
}

// Filter selects events for Read. Each field that is set narrows the
// result.
type Filter struct {
	// Subject keeps only the events with this subject.
	Subject string
	// Types keeps only the events of these types.
	Types []string
	// After keeps only the events after this position; 0 keeps them from
	// the start.
	After uint64
	// Limit keeps at most this many events; 0 keeps them all.
	Limit int
}

// Entry is an event of the log and its position.
type Entry struct {
	Position uint64
	Event    *cloudeventsv1.CloudEvent
}

// Read returns the committed events that match f in log order, and the
// head position when it read them.
func (l *Log) Read(f Filter) ([]Entry, uint64, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return nil, 0, ErrClosed
	}
	head := uint64(len(l.records))
	entries := []Entry{}
	for _, p := range l.candidates(f) {
		if p <= f.After {
			continue
		}
		r := l.records[p-1]
		if len(f.Types) > 0 && !slices.Contains(f.Types, r.typ) {
			continue
		}
		if f.Subject != "" && r.subject != f.Subject {
			continue
		}
		e, err := l.event(p, r)
		if err != nil {
			return nil, 0, err
		}
		entries = append(entries, Entry{Position: p, Event: e})
		if f.Limit > 0 && len(entries) == f.Limit {
			break
		}
	}
	return entries, head, nil
}

// candidates returns positions in order that include every event that
// matches f, starting at the first position after f.After: the positions
// of f.Subject when it is set, else those of f.Types when it names one
// type, else every position.
func (l *Log) candidates(f Filter) []uint64 {
	var positions []uint64
	switch {
	case f.Subject != "":
		positions = l.bySubject[f.Subject]
	case len(f.Types) == 1:
		positions = l.byType[f.Types[0]]
	default:
		positions = make([]uint64, 0, uint64(len(l.records))-min(f.After, uint64(len(l.records))))
		for p := f.After + 1; p <= uint64(len(l.records)); p++ {
			positions = append(positions, p)
		}
		return positions
	}
	i, _ := slices.BinarySearch(positions, f.After+1)
	return positions[i:]
}

// event reads the committed event at position p, whose record is r.
func (l *Log) event(p uint64, r record) (*cloudeventsv1.CloudEvent, error) {
	line := make([]byte, r.length)
	if _, err := l.file.ReadAt(line, r.offset); err != nil {
		return nil, fmt.Errorf("%s line %d: %w", filepath.Join(l.dir, logName), p, err)
	}
	var e cloudeventsv1.CloudEvent
	if err := protojson.Unmarshal(line, &e); err != nil {
		return nil, fmt.Errorf("%s line %d: %w", filepath.Join(l.dir, logName), p, err)
	}
	return &e, nil
}

// Append writes evs to the end of the log, all or none, and returns their
// positions. It writes each event's tenant attribute as Tenant and its
// prevhash attribute as the hash of the line before it, on copies, so the
// caller's events do not change.
//
// When check is set, Append calls it first, with a function that gives a
// subject's version, while no other append can run, and writes nothing if
// it returns an error, which Append returns.
//
// The lines of all the events are written in one write and synced; then
// head is replaced by writing head.tmp, syncing it and renaming it. If a
// step fails before the rename, Append cuts log.jsonl back to its committed
// end and returns the error, and the log is as it was. If only the sync of
// the directory after the rename fails, Append returns the positions with
// the error, because the events are committed in the open Log but may not
// survive a crash, and the Log takes no more appends.
func (l *Log) Append(evs []*cloudeventsv1.CloudEvent, check func(version func(subject string) uint64) error) ([]uint64, error) {
	return l.append(evs, nil, check)
}

// ErrHeadMoved is the error of an AppendAt whose head is no longer the
// log's head.
var ErrHeadMoved = errors.New("the head of the event log moved")

// AppendAt is Append, except that it writes nothing and returns
// ErrHeadMoved unless the head is still at the position at. A caller
// that read the log at head, such as the control plane projecting the
// facts of a decision, writes with it only what it decided from that read.
func (l *Log) AppendAt(at uint64, evs []*cloudeventsv1.CloudEvent, check func(version func(subject string) uint64) error) ([]uint64, error) {
	return l.append(evs, &at, check)
}

// Snapshot returns the position of the last committed event and the hash
// of its line, sha256:<hex>, read together; 0 and "" when the log is
// empty.
func (l *Log) Snapshot() (uint64, string) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return uint64(len(l.records)), l.hash
}

// append is Append and AppendAt: when at is set, it writes only while the
// head is at *at.
func (l *Log) append(evs []*cloudeventsv1.CloudEvent, at *uint64, check func(version func(subject string) uint64) error) ([]uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case l.closed:
		return nil, ErrClosed
	case l.broken != nil:
		return nil, l.broken
	case len(evs) == 0:
		return nil, errors.New("an append needs at least one event")
	case at != nil && *at != uint64(len(l.records)):
		return nil, ErrHeadMoved
	}
	if check != nil {
		if err := check(l.version); err != nil {
			return nil, err
		}
	}
	buf, records, hash, err := l.encode(evs)
	if err != nil {
		return nil, err
	}

	l.step("write log")
	if _, err := l.file.WriteAt(buf, l.end); err != nil {
		return nil, l.undo(fmt.Errorf("writing %s: %w", filepath.Join(l.dir, logName), err))
	}
	l.step("sync log")
	if err := l.file.Sync(); err != nil {
		return nil, l.undo(fmt.Errorf("syncing %s: %w", filepath.Join(l.dir, logName), err))
	}
	newHead := head{Position: uint64(len(l.records) + len(records)), Hash: hash}
	if err := l.writeTemp(newHead); err != nil {
		return nil, l.undo(err)
	}
	l.step("rename head.tmp to head")
	if err := l.root.Rename(tempName, headName); err != nil {
		return nil, l.undo(fmt.Errorf("replacing %s: %w", filepath.Join(l.dir, headName), err))
	}

	positions := l.commit(records, hash, int64(len(buf)))
	l.step("sync directory")
	if err := syncDir(l.root); err != nil {
		// The rename is done, so the append may survive a crash or may not.
		// No later append can be trusted to chain to it.
		l.broken = fmt.Errorf("the event log in %s stopped taking appends after its directory could not be synced: %w", l.dir, err)
		return positions, l.broken
	}
	return positions, nil
}

// step calls l.trace with step when it is set.
func (l *Log) step(step string) {
	if l.trace != nil {
		l.trace(step)
	}
}

// encode returns the lines of evs, each ended by a newline, their records,
// and the hash of the last line. It works on copies of the events.
func (l *Log) encode(evs []*cloudeventsv1.CloudEvent) ([]byte, []record, string, error) {
	var buf bytes.Buffer
	records := make([]record, 0, len(evs))
	hash := l.hash
	for i, original := range evs {
		e, ok := proto.Clone(original).(*cloudeventsv1.CloudEvent)
		if !ok || e == nil {
			return nil, nil, "", fmt.Errorf("event %d of the append is nil", i+1)
		}
		if e.Attributes == nil {
			e.Attributes = map[string]*cloudeventsv1.CloudEvent_CloudEventAttributeValue{}
		}
		e.Attributes[events.ExtensionTenant] = stringValue(Tenant)
		delete(e.Attributes, events.ExtensionPrevHash)
		if hash != "" {
			e.Attributes[events.ExtensionPrevHash] = stringValue(hash)
		}
		if err := events.Validate(e); err != nil {
			return nil, nil, "", fmt.Errorf("event %d of the append: %w", i+1, err)
		}
		line, err := protojson.Marshal(e)
		if err != nil {
			return nil, nil, "", fmt.Errorf("event %d of the append: %w", i+1, err)
		}
		if bytes.IndexByte(line, '\n') >= 0 {
			return nil, nil, "", fmt.Errorf("event %d of the append: its protojson has a newline", i+1)
		}
		records = append(records, record{
			offset:  l.end + int64(buf.Len()),
			length:  len(line),
			typ:     e.GetType(),
			subject: e.GetAttributes()["subject"].GetCeString(),
		})
		buf.Write(line)
		buf.WriteByte('\n')
		hash = hashOf(line)
	}
	return buf.Bytes(), records, hash, nil
}

// undo cuts log.jsonl back to its committed end after a failed append and
// returns cause. If the cut fails, the Log stops taking appends.
func (l *Log) undo(cause error) error {
	if err := l.file.Truncate(l.end); err != nil {
		l.broken = fmt.Errorf("the event log in %s stopped taking appends after a failed append could not be undone: %w",
			l.dir, errors.Join(cause, err))
		return l.broken
	}
	if err := l.file.Sync(); err != nil {
		l.broken = fmt.Errorf("the event log in %s stopped taking appends after a failed append could not be undone: %w",
			l.dir, errors.Join(cause, err))
		return l.broken
	}
	return cause
}

// commit adds records, which end with the line of hash, to the committed
// log, whose size grows by size, and returns their positions.
func (l *Log) commit(records []record, hash string, size int64) []uint64 {
	positions := make([]uint64, 0, len(records))
	for _, r := range records {
		positions = append(positions, l.add(r))
	}
	l.hash = hash
	l.end += size
	close(l.changed)
	l.changed = make(chan struct{})
	return positions
}

// add adds r to the records and the indexes and returns its position.
func (l *Log) add(r record) uint64 {
	l.records = append(l.records, r)
	p := uint64(len(l.records))
	if r.subject != "" {
		l.bySubject[r.subject] = append(l.bySubject[r.subject], p)
	}
	l.byType[r.typ] = append(l.byType[r.typ], p)
	return p
}

// stringValue is a string attribute value.
func stringValue(s string) *cloudeventsv1.CloudEvent_CloudEventAttributeValue {
	return &cloudeventsv1.CloudEvent_CloudEventAttributeValue{
		Attr: &cloudeventsv1.CloudEvent_CloudEventAttributeValue_CeString{CeString: s},
	}
}

// hashOf is sha256: and the hex SHA-256 of line, the form of prevhash
// (decision 0133).
func hashOf(line []byte) string {
	sum := sha256.Sum256(line)
	return "sha256:" + hex.EncodeToString(sum[:])
}
