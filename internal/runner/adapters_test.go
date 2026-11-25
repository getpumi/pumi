// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// fakeAdapterName is the name of the fixture's adapter, which runs its
// model role, frontier, and the checks (decisions 0245 and 0253).
const fakeAdapterName = "fake"

// fakeHarnessVersion is the version of its harness that the fake adapter
// writes to its standard error, which is the log (decision 0245).
const fakeHarnessVersion = "fake harness 1.2.3"

// selfHash is the SHA-256 of the test binary, which is the fake adapter,
// in lowercase hexadecimal.
var selfHash = sync.OnceValues(func() (string, error) {
	b, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
})

// fakeAdapterHash returns the SHA-256 of the test binary.
func fakeAdapterHash(t *testing.T) string {
	t.Helper()
	h, err := selfHash()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// copyAdapter copies the test binary to an executable file named name in a
// new directory and returns its path, so that a test has a second adapter
// that the log can tell apart by its path.
func copyAdapter(t *testing.T, name string) string {
	t.Helper()
	src, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := root.WriteFile(name, src, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}

// startedPath returns the path that the fake adapter says it was started
// as, from the log.
func startedPath(t *testing.T, log string) string {
	t.Helper()
	for line := range strings.SplitSeq(log, "\n") {
		if p, ok := strings.CutPrefix(line, fakeAdapterPrefix+"started as "); ok {
			return p
		}
	}
	t.Fatalf("the log does not say which adapter started; it is\n%s", log)
	return ""
}

// TestTheModelRolePicksTheAdapter: two model roles on two adapters; an
// agent run starts the adapter of its model role, and the runner opens it
// with the gateway for that adapter (decision 0245).
func TestTheModelRolePicksTheAdapter(t *testing.T) {
	second := copyAdapter(t, "second-adapter")
	for _, tc := range []struct{ role, adapter, path string }{
		{"frontier", fakeAdapterName, "/proc/self/exe"},
		{"review", "second", second},
	} {
		t.Run(tc.role, func(t *testing.T) {
			f := newFixture(t, "success")
			f.cfg.Adapters["second"] = Adapter{Path: second, SHA256: fakeAdapterHash(t)}
			f.cfg.Roles["review"] = "second"
			f.service.claim.Invocation.ModelRole = tc.role
			f.checkPicked(t, tc.role, tc.adapter, tc.path)
		})
	}
}

// checkPicked runs the fixture's invocation, which has the model role
// role, and checks that the runner started the adapter named adapter at
// path and opened the gateway for it.
func (f *fixture) checkPicked(t *testing.T, role, adapter, path string) {
	t.Helper()
	report := f.runOne(t)
	log := f.service.log()
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Fatalf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), log)
	}
	if got := startedPath(t, log); got != path {
		t.Errorf("the runner started %s, want %s", got, path)
	}
	if !strings.Contains(log, runnerPrefix+"the adapter "+adapter+" (") {
		t.Errorf("the log does not name the adapter %s; it is\n%s", adapter, log)
	}
	if got := f.gateway.adapters(); !slices.Equal(got, []string{adapter}) {
		t.Errorf("the runner opened the gateway for the adapters %q, want %s", got, adapter)
	}
	if inv := sentInvocation(t, log); inv.GetModelRole() != role {
		t.Errorf("the harness invocation has the model role %q, want %s", inv.GetModelRole(), role)
	}
}

// TestTheGatewaySaysPerAdapterWhetherTheHarnessKeepsItsSignIn: the harness
// invocation says that the harness keeps its sign-in when its adapter is
// in the sign-in mode, and not when another adapter is (decision 0240).
func TestTheGatewaySaysPerAdapterWhetherTheHarnessKeepsItsSignIn(t *testing.T) {
	second := copyAdapter(t, "second-adapter")
	for _, tc := range []struct {
		role  string
		keeps bool
	}{{"frontier", false}, {"review", true}} {
		t.Run(tc.role, func(t *testing.T) {
			f := newFixture(t, "success")
			f.gateway.signIn = map[string]bool{"second": true}
			f.cfg.Adapters["second"] = Adapter{Path: second, SHA256: fakeAdapterHash(t), SignIn: true}
			f.cfg.Roles["review"] = "second"
			f.service.claim.Invocation.ModelRole = tc.role
			f.runOne(t)
			if got := sentInvocation(t, f.service.log()).GetGateway().GetKeepsSignIn(); got != tc.keeps {
				t.Errorf("keeps_sign_in is %v, want %v", got, tc.keeps)
			}
		})
	}
}

// TestACheckRunsOnTheCheckAdapter: a check names no model role, so the
// runner starts the check adapter (decision 0253).
func TestACheckRunsOnTheCheckAdapter(t *testing.T) {
	checks := copyAdapter(t, "check-adapter")
	f := newFixture(t, "success")
	f.cfg.Adapters["checks"] = Adapter{Path: checks, SHA256: fakeAdapterHash(t)}
	f.cfg.CheckAdapter = "checks"
	f.service.claim.Invocation.Component.Kind = harnessv1.Component_KIND_CHECK
	f.service.claim.Invocation.ModelRole = ""
	f.service.claim.Invocation.Capabilities.Network.Access = harnessv1.Capabilities_Network_ACCESS_NONE
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Fatalf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if got := startedPath(t, f.service.log()); got != checks {
		t.Errorf("the runner started %s, want the check adapter %s", got, checks)
	}
}

// TestAModelRoleWithNoAdapterFailsTheInvocation: an agent run whose model
// role the runner has no adapter for fails before any adapter starts.
func TestAModelRoleWithNoAdapterFailsTheInvocation(t *testing.T) {
	f := newFixture(t, "success")
	f.service.claim.Invocation.ModelRole = "unknown"
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed", report.GetOutcome())
	}
	if log := f.service.log(); !strings.Contains(log, `model role "unknown"`) {
		t.Errorf("the log does not name the model role; it is\n%s", log)
	}
	f.checkNotStarted(t)
}

// TestAnAdapterWhoseHashDiffersIsRefused: before it starts an adapter, the
// runner checks the executable's SHA-256 and refuses one that does not
// match, with an error that names the adapter and both hashes; it opens no
// gateway and starts nothing (decision 0245).
func TestAnAdapterWhoseHashDiffersIsRefused(t *testing.T) {
	f := newFixture(t, "success")
	pinned := strings.Repeat("0", 64)
	f.cfg.Adapters[fakeAdapterName] = Adapter{Path: "/proc/self/exe", SHA256: pinned}
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed", report.GetOutcome())
	}
	log := f.service.log()
	for _, want := range []string{"adapter " + fakeAdapterName, pinned, fakeAdapterHash(t)} {
		if !strings.Contains(log, want) {
			t.Errorf("the log does not hold %q; it is\n%s", want, log)
		}
	}
	f.checkNotStarted(t)
	if opened, _ := f.gateway.state(); len(opened) > 0 {
		t.Errorf("the runner opened %q with the gateway, want nothing", opened)
	}
}

// TestTheLogNamesTheAdapterAndItsHarnessVersion: the runner writes the
// adapter's name, path and SHA-256 to the log before it starts it, and the
// harness version that the adapter writes to its standard error is in the
// log (decision 0245).
func TestTheLogNamesTheAdapterAndItsHarnessVersion(t *testing.T) {
	f := newFixture(t, "success")
	f.runOne(t)
	log := f.service.log()
	want := runnerPrefix + "the adapter " + fakeAdapterName + " (/proc/self/exe, SHA-256 " + fakeAdapterHash(t) + ") runs the invocation plan-1/1"
	for _, w := range []string{want, fakeAdapterPrefix + fakeHarnessVersion} {
		if !strings.Contains(log, w) {
			t.Errorf("the log does not hold %q; it is\n%s", w, log)
		}
	}
}

// TestRemoteModeRefusesASignInAdapter: in remote mode the runner refuses a
// configuration with an adapter in the sign-in mode, before it claims
// anything (decision 0240).
func TestRemoteModeRefusesASignInAdapter(t *testing.T) {
	f := newFixture(t, "success")
	f.cfg.Adapters["mine"] = Adapter{Path: "/proc/self/exe", SHA256: fakeAdapterHash(t), SignIn: true}
	f.cfg.Remote = true
	ran, err := RunOne(t.Context(), f.cfg)
	if err == nil || !strings.Contains(err.Error(), `"mine"`) || !strings.Contains(err.Error(), "remote mode") {
		t.Errorf("RunOne = %v, %v; want an error that names the adapter mine and remote mode", ran, err)
	}
	if f.service.claim == nil {
		t.Error("the runner claimed the invocation, want no claim")
	}
	f.cfg.Adapters["mine"] = Adapter{Path: "/proc/self/exe", SHA256: fakeAdapterHash(t)}
	if ran, err := RunOne(t.Context(), f.cfg); err != nil || !ran {
		t.Errorf("RunOne with no adapter in the sign-in mode = %v, %v; want true, nil", ran, err)
	}
}
