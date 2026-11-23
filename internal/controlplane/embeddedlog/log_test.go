// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package embeddedlog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
)

// attr returns the string attribute name of e, or "" when it has none.
func attr(e *cloudeventsv1.CloudEvent, name string) string {
	return e.GetAttributes()[name].GetCeString()
}

func TestWorktreeAndMainCloneOpenTheSameLog(t *testing.T) {
	main := newClone(t)
	worktree := addWorktree(t, main)

	l := open(t, worktree)
	want, err := filepath.EvalSymlinks(filepath.Join(main, ".git", "pumi"))
	if err == nil {
		got, _ := filepath.EvalSymlinks(l.Dir())
		if got != want {
			t.Errorf("the log of the worktree is in %s, want %s", got, want)
		}
	} else {
		t.Fatalf("the worktree's log is not in the main clone's git directory: %v", err)
	}
	mustAppend(t, l, planAdded(t, "e1", "plan-1"))
	closeLog(t, l)

	l = open(t, main)
	entries, head, err := l.Read(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(entries); !slices.Equal(got, []string{"1:e1"}) || head != 1 {
		t.Errorf("the main clone reads %v with head %d, want [1:e1] with head 1", got, head)
	}
}

func TestOpenOutsideAGitCloneFails(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	if l, err := Open(t.Context(), t.TempDir()); err == nil {
		_ = l.Close()
		t.Fatal("Open succeeded outside a git clone")
	}
}

func TestEachEventIsOneLineChainedByPrevhash(t *testing.T) {
	l := open(t, newClone(t))
	first := mustAppend(t, l, planAdded(t, "e1", "plan-1"), planAdded(t, "e2", "plan-2"), leaseGranted(t, "e3", "lease-1"))
	second := mustAppend(t, l, planAdded(t, "e4", "plan-1"))
	if !slices.Equal(first, []uint64{1, 2, 3}) || !slices.Equal(second, []uint64{4}) {
		t.Fatalf("positions %v and %v, want [1 2 3] and [4]", first, second)
	}

	lines := readLines(t, l.Dir())
	if len(lines) != 4 {
		t.Fatalf("log.jsonl has %d lines, want 4", len(lines))
	}
	for i, line := range lines {
		want := ""
		if i > 0 {
			want = hashOf(lines[i-1])
		}
		checkLine(t, i+1, line, want)
	}

	var h struct {
		Position uint64 `json:"position"`
		Hash     string `json:"hash"`
	}
	b, err := os.ReadFile(filepath.Join(l.Dir(), "head"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatalf("head is not JSON: %v", err)
	}
	if h.Position != 4 || h.Hash != hashOf(lines[3]) {
		t.Errorf("head is %+v, want position 4 and the hash of line 4", h)
	}
}

// checkLine checks that line n is the protojson of a valid Pumi event of
// the tenant Tenant whose prevhash is prevhash.
func checkLine(t *testing.T, n int, line []byte, prevhash string) {
	t.Helper()
	var e cloudeventsv1.CloudEvent
	if err := protojson.Unmarshal(line, &e); err != nil {
		t.Fatalf("line %d is not the protojson of a CloudEvent: %v", n, err)
	}
	if err := events.Validate(&e); err != nil {
		t.Errorf("line %d: %v", n, err)
	}
	if got := attr(&e, events.ExtensionTenant); got != Tenant {
		t.Errorf("line %d has the tenant %q, want %q", n, got, Tenant)
	}
	if got := attr(&e, events.ExtensionPrevHash); got != prevhash {
		t.Errorf("line %d has the prevhash %q, want %q", n, got, prevhash)
	}
}

func TestAppendWritesOnceSyncsThenReplacesHead(t *testing.T) {
	l := open(t, newClone(t))
	var steps []string
	l.trace = func(step string) { steps = append(steps, step) }
	mustAppend(t, l, planAdded(t, "e1", "plan-1"), planAdded(t, "e2", "plan-2"))
	want := []string{"write log", "sync log", "write head.tmp", "sync head.tmp", "rename head.tmp to head", "sync directory"}
	if !slices.Equal(steps, want) {
		t.Errorf("an append of two events took the steps %q, want %q", steps, want)
	}
}

func TestAppendDoesNotChangeTheCallersEvents(t *testing.T) {
	l := open(t, newClone(t))
	mustAppend(t, l, planAdded(t, "e1", "plan-1"))
	e := planAdded(t, "e2", "plan-1")
	mustAppend(t, l, e)
	if got := attr(e, events.ExtensionTenant); got != "set by the caller" {
		t.Errorf("Append changed the caller's tenant to %q", got)
	}
	if got := attr(e, events.ExtensionPrevHash); got != "" {
		t.Errorf("Append set the caller's prevhash to %q", got)
	}
}

func TestAppendRefusedByItsCheckWritesNothing(t *testing.T) {
	l := open(t, newClone(t))
	mustAppend(t, l, planAdded(t, "e1", "plan-1"))
	refused := errors.New("plan-1 is not at version 0")
	_, err := l.Append([]*cloudeventsv1.CloudEvent{planAdded(t, "e2", "plan-2")}, func(version func(string) uint64) error {
		if version("plan-1") != 0 {
			return refused
		}
		return nil
	})
	if !errors.Is(err, refused) {
		t.Fatalf("Append returned %v, want the check's error", err)
	}
	if got := len(readLines(t, l.Dir())); got != 1 || l.Head() != 1 {
		t.Errorf("after a refused append the log has %d lines and head %d, want 1 and 1", got, l.Head())
	}
}

func TestAppendRefusesAnInvalidEventAndWritesNothing(t *testing.T) {
	l := open(t, newClone(t))
	bad := planAdded(t, "e2", "plan-1")
	bad.Id = ""
	if _, err := l.Append([]*cloudeventsv1.CloudEvent{planAdded(t, "e1", "plan-1"), bad}, nil); err == nil {
		t.Fatal("Append accepted an event with no id")
	}
	if _, err := l.Append(nil, nil); err == nil {
		t.Fatal("Append accepted no events")
	}
	if l.Head() != 0 || len(readLines(t, l.Dir())) != 0 {
		t.Error("a refused append wrote to the log")
	}
}

func TestAFailedHeadWriteUndoesTheAppend(t *testing.T) {
	dir := newClone(t)
	l := open(t, dir)
	mustAppend(t, l, planAdded(t, "e1", "plan-1"))
	// A directory in the place of head.tmp makes writing it fail.
	if err := os.Mkdir(filepath.Join(l.Dir(), "head.tmp"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append([]*cloudeventsv1.CloudEvent{planAdded(t, "e2", "plan-1")}, nil); err == nil {
		t.Fatal("Append succeeded without writing head")
	}
	if got := len(readLines(t, l.Dir())); got != 1 || l.Head() != 1 || l.Version("plan-1") != 1 {
		t.Errorf("after a failed append the log has %d lines, head %d and plan-1 at version %d, want 1, 1 and 1",
			got, l.Head(), l.Version("plan-1"))
	}
	if err := os.Remove(filepath.Join(l.Dir(), "head.tmp")); err != nil {
		t.Fatal(err)
	}
	if got := mustAppend(t, l, planAdded(t, "e3", "plan-1")); !slices.Equal(got, []uint64{2}) {
		t.Errorf("the next append got positions %v, want [2]", got)
	}
}

func TestLinesAfterTheCommittedHeadAreDroppedOnOpen(t *testing.T) {
	dir := newClone(t)
	l := open(t, dir)
	mustAppend(t, l, planAdded(t, "e1", "plan-1"), planAdded(t, "e2", "plan-1"))
	pumi := l.Dir()
	closeLog(t, l)

	// An append that wrote its lines and never replaced head, followed by a
	// line that was cut off.
	lines := readLines(t, pumi)
	f, err := openRoot(t, pumi).OpenFile("log.jsonl", os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(append(lines[1], '\n'), []byte(`{"id":"cut`)...)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	l = open(t, dir)
	if l.Head() != 2 || l.Version("plan-1") != 2 {
		t.Errorf("after open the head is %d and plan-1 is at version %d, want 2 and 2", l.Head(), l.Version("plan-1"))
	}
	if got := readLines(t, pumi); len(got) != 2 {
		t.Errorf("after open log.jsonl has %d lines, want 2", len(got))
	}
	if got := mustAppend(t, l, planAdded(t, "e3", "plan-2")); !slices.Equal(got, []uint64{3}) {
		t.Errorf("the next append got positions %v, want [3]", got)
	}
	lines = readLines(t, pumi)
	entries, _, err := l.Read(Filter{After: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := attr(entries[0].Event, events.ExtensionPrevHash); got != hashOf(lines[1]) {
		t.Errorf("the event after the dropped lines has the prevhash %q, want the hash of line 2", got)
	}
}

func TestIndexesAreRebuiltOnOpen(t *testing.T) {
	dir := newClone(t)
	l := open(t, dir)
	mustAppend(t, l, planAdded(t, "e1", "plan-1"), leaseGranted(t, "e2", "lease-1"))
	mustAppend(t, l, planAdded(t, "e3", "plan-2"), leaseGranted(t, "e4", "plan-1"))
	mustAppend(t, l, planAdded(t, "e5", "plan-1"))
	closeLog(t, l)

	l = open(t, dir)
	for subject, want := range map[string]uint64{"plan-1": 3, "plan-2": 1, "lease-1": 1, "plan-3": 0} {
		if got := l.Version(subject); got != want {
			t.Errorf("Version(%q) = %d, want %d", subject, got, want)
		}
	}
	for _, c := range []struct {
		name   string
		filter Filter
		want   []string
	}{
		{"all", Filter{}, []string{"1:e1", "2:e2", "3:e3", "4:e4", "5:e5"}},
		{"subject", Filter{Subject: "plan-1"}, []string{"1:e1", "4:e4", "5:e5"}},
		{"one type", Filter{Types: []string{events.TypeLeaseGranted}}, []string{"2:e2", "4:e4"}},
		{"two types", Filter{Types: []string{events.TypeLeaseGranted, events.TypePlanAdded}}, []string{"1:e1", "2:e2", "3:e3", "4:e4", "5:e5"}},
		{"subject and type", Filter{Subject: "plan-1", Types: []string{events.TypePlanAdded}}, []string{"1:e1", "5:e5"}},
		{"after", Filter{After: 3}, []string{"4:e4", "5:e5"}},
		{"subject after", Filter{Subject: "plan-1", After: 1}, []string{"4:e4", "5:e5"}},
		{"limit", Filter{Limit: 2}, []string{"1:e1", "2:e2"}},
		{"after and limit", Filter{After: 1, Limit: 2, Types: []string{events.TypePlanAdded}}, []string{"3:e3", "5:e5"}},
		{"after the head", Filter{After: 5}, []string{}},
		{"unknown subject", Filter{Subject: "plan-3"}, []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			entries, head, err := l.Read(c.filter)
			if err != nil {
				t.Fatal(err)
			}
			if got := ids(entries); !slices.Equal(got, c.want) || head != 5 {
				t.Errorf("Read(%+v) = %v with head %d, want %v with head 5", c.filter, got, head, c.want)
			}
		})
	}
}

// TestDirOfNamesTheDirectoryOfTheLog: DirOf gives the pumi directory that
// Open uses, from a worktree too, without opening the log, so that a pumi
// command finds the socket of a run that holds the log (decision 0206).
func TestDirOfNamesTheDirectoryOfTheLog(t *testing.T) {
	main := newClone(t)
	worktree := addWorktree(t, main)
	got, err := DirOf(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	l := open(t, main)
	defer closeLog(t, l)
	want, _ := filepath.EvalSymlinks(l.Dir())
	if got, _ = filepath.EvalSymlinks(got); got != want {
		t.Errorf("DirOf(the worktree) = %s, want %s", got, want)
	}
}
