// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) != "" {
		os.Exit(fakeAdapter(os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// fakeTurn turns a step into the fake harness's tool calls. On the
// Responses API a write is a freeform tool call and the structured output
// is the answer's text, as Codex gives it; on the Messages API both are
// tool calls with JSON input, as Claude Code's are.
func fakeTurn(api API) func(Step) Turn {
	return func(s Step) Turn {
		call := func(name, input string) Turn { return Turn{Calls: []ToolCall{{Name: name, Input: input}}} }
		switch s.Kind {
		case StepWrite:
			if api == APIResponses {
				return Turn{Calls: []ToolCall{{Name: toolPatch, Input: s.Path + "\n" + s.Content, Freeform: true}}}
			}
			return call(toolWrite, jsonObject("path", s.Path, "content", s.Content))
		case StepRead:
			return call(toolRead, jsonObject("path", s.Path))
		case StepRun:
			return call(toolRun, jsonObject("command", s.Command))
		case StepFetch:
			return call(toolFetch, jsonObject("url", s.URL))
		case StepOutput:
			if api == APIResponses {
				return Turn{Text: s.Output}
			}
			return call(toolOutput, `{"output":`+s.Output+`}`)
		}
		return Turn{Text: "unknown step"}
	}
}

// jsonObject returns a JSON object of the string fields kv, given as name
// and value in turn.
func jsonObject(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, fmt.Sprintf("%q:%q", kv[i], kv[i+1]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// fakeAdapterOf returns the fake adapter on api, offering modes, with the
// fault fault, or none when it is empty. The test binary is the adapter,
// at the constant path of the running program.
func fakeAdapterOf(api API, modes []Mode, fault string) Adapter {
	name := "messages"
	if api == APIResponses {
		name = responsesAPIName
	}
	return Adapter{
		Path: "/proc/self/exe", API: api, Modes: modes, Turn: fakeTurn(api),
		SignIn: func(base, credential string) []string {
			return []string{fakeProviderEnv + "=" + base, fakeSignInEnv + "=" + credential}
		},
		Env:             []string{fakeEnv + "=1", fakeAPIEnv + "=" + name, fakeFaultEnv + "=" + fault},
		NestedSession:   []string{fakeNestedEnv + "=1"},
		ReportsRefusals: true, ReportsLimits: true,
		// A test binary built with -race takes about a second to start.
		Timeout: 30 * time.Second, StopGrace: 10 * time.Second,
	}
}

var apis = []API{APIMessages, APIResponses}

// TestRunPassesAGoodAdapter runs the suite against the fake adapter
// without a fault, on each API, with each credential mode first.
func TestRunPassesAGoodAdapter(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the fake adapter is the test binary at /proc/self/exe, which only Linux has")
	}
	for _, api := range apis {
		for _, modes := range [][]Mode{{ModeAPIKey, ModeSignIn}, {ModeSignIn, ModeAPIKey}} {
			t.Run(fmt.Sprintf("%v, %v first", api, modes[0]), func(t *testing.T) {
				t.Parallel()
				Run(t, fakeAdapterOf(api, modes, ""))
			})
		}
	}
}

// probe is a T that records what a scenario reports, so that a test can
// check that the scenario fails.
type probe struct {
	t        *testing.T
	mu       sync.Mutex
	failed   bool
	skipped  bool
	messages []string
	cleanups []func()
}

func (p *probe) Helper()                  {}
func (p *probe) Context() context.Context { return p.t.Context() }
func (p *probe) TempDir() string          { return p.t.TempDir() }

func (p *probe) Logf(format string, args ...any) { p.note(false, format, args...) }

func (p *probe) Errorf(format string, args ...any) { p.note(true, format, args...) }

func (p *probe) Fatalf(format string, args ...any) {
	p.note(true, format, args...)
	runtime.Goexit()
}

func (p *probe) Skipf(format string, args ...any) {
	p.mu.Lock()
	p.skipped = true
	p.mu.Unlock()
	p.note(false, format, args...)
	runtime.Goexit()
}

func (p *probe) Failed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failed
}

func (p *probe) Cleanup(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanups = append(p.cleanups, f)
}

func (p *probe) note(failed bool, format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failed = p.failed || failed
	p.messages = append(p.messages, fmt.Sprintf(format, args...))
}

// playProbe plays the scenario named name against a with a probe, and
// returns the probe once the scenario's cleanups have run.
func playProbe(t *testing.T, a *Adapter, name string) *probe {
	t.Helper()
	var s scenario
	for _, x := range scenarios {
		if x.name == name {
			s = x
		}
	}
	if s.name == "" {
		t.Fatalf("no scenario %q", name)
	}
	p := &probe{t: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		play(p, a, s)
	}()
	<-done
	for i := len(p.cleanups) - 1; i >= 0; i-- {
		p.cleanups[i]()
	}
	return p
}

// faults are the faults of the fake adapter, each with the scenario that
// must catch it and the API it runs on.
var faults = []struct {
	fault, scenario string
	api             API
}{
	{"exit-status", "result", APIMessages},
	{"no-result", "result", APIResponses},
	{"bad-line", "result", APIMessages},
	{"line-after-result", "result", APIResponses},
	{"wrong-output", "output-schema", APIMessages},
	{"wrong-output", "output-schema", APIResponses},
	{"not-failed", "failed", APIMessages},
	{"ignore-stdin", "stop", APIResponses},
	{"write-anywhere", "write-paths", APIMessages},
	{"no-refusals", "write-paths", APIResponses},
	{"unanchored", "anchored-paths", APIResponses},
	{"commands-any", "commands", APIMessages},
	{"chain", "commands", APIResponses},
	{"assignments-ignored", "assignments", APIMessages},
	{"assignments-ignored", "assignments-unlisted", APIResponses},
	{"network-any", "network", APIMessages},
	{"tool-read", "tool-read", APIResponses},
	{"tool-edit", "tool-edit", APIMessages},
	{"tool-run", "tool-run", APIResponses},
	{"tool-web", "tool-web", APIMessages},
	{"nested", "nested-session", APIResponses},
	{"token", "calls-api-key", APIMessages},
	{"sign-in-leak", "calls-api-key", APIResponses},
	{"elsewhere", "calls-api-key", APIMessages},
	{"model", "calls-sign-in", APIResponses},
	{"no-credential", "calls-sign-in", APIMessages},
	{"no-usage", "usage-sign-in", APIResponses},
	{"wrong-usage", "usage-sign-in", APIMessages},
	{"wrong-usage", "usage-sign-in", APIResponses},
	{"wait-limit", "limit", APIMessages},
}

// TestEachScenarioFailsAnAdapterThatBreaksIt runs each fault of the fake
// adapter against the scenario that checks its rule, which must fail.
func TestEachScenarioFailsAnAdapterThatBreaksIt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the fake adapter is the test binary at /proc/self/exe, which only Linux has")
	}
	for _, f := range faults {
		t.Run(fmt.Sprintf("%s in %s on the %v", f.fault, f.scenario, f.api), func(t *testing.T) {
			t.Parallel()
			a := fakeAdapterOf(f.api, []Mode{ModeAPIKey, ModeSignIn}, f.fault)
			// A fault that waits must not keep the test long.
			a.Timeout, a.StopGrace = 10*time.Second, 5*time.Second
			p := playProbe(t, &a, f.scenario)
			if !p.failed || p.skipped {
				t.Errorf("the scenario did not fail; it reported:\n%s", strings.Join(p.messages, "\n"))
				return
			}
			t.Logf("the scenario failed with:\n%s", strings.Join(p.messages, "\n"))
		})
	}
}

// TestEveryScenarioHasAFault checks that the faults cover every scenario
// whose rule an adapter can break.
func TestEveryScenarioHasAFault(t *testing.T) {
	for _, s := range scenarios {
		found := false
		for _, f := range faults {
			found = found || f.scenario == s.name
		}
		if !found {
			t.Errorf("no fault of the fake adapter breaks the scenario %s", s.name)
		}
	}
}

func TestScenariosAreSkippedForWhatTheAdapterSays(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the fake adapter is the test binary at /proc/self/exe, which only Linux has")
	}
	unenforced := fakeAdapterOf(APIResponses, []Mode{ModeAPIKey}, "write-anywhere")
	unenforced.Unenforced = map[Item]string{ItemWritePaths: "the sandbox does not limit writes to some paths"}
	noLimits := fakeAdapterOf(APIMessages, []Mode{ModeAPIKey}, "")
	noLimits.ReportsLimits = false
	for _, c := range []struct {
		name     string
		a        Adapter
		scenario string
	}{
		{"unenforced item", unenforced, "write-paths"},
		{"no sign-in mode", fakeAdapterOf(APIMessages, []Mode{ModeAPIKey}, ""), "usage-sign-in"},
		{"no API key mode", fakeAdapterOf(APIMessages, []Mode{ModeSignIn}, ""), "calls-api-key"},
		{"limits not reported", noLimits, "limit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if p := playProbe(t, &c.a, c.scenario); !p.skipped || p.failed {
				t.Errorf("the scenario was not skipped: %s", strings.Join(p.messages, "\n"))
			}
		})
	}
}

func TestRefusalsAreCheckedOnlyWhereTheAdapterSaysItsHarnessReportsThem(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the fake adapter is the test binary at /proc/self/exe, which only Linux has")
	}
	a := fakeAdapterOf(APIMessages, []Mode{ModeAPIKey}, "no-refusals")
	a.ReportsRefusals = false
	if p := playProbe(t, &a, "write-paths"); p.failed {
		t.Errorf("the scenario failed: %s", strings.Join(p.messages, "\n"))
	}
}

func TestRunRefusesAnAdapterItCannotRun(t *testing.T) {
	good := fakeAdapterOf(APIMessages, []Mode{ModeAPIKey, ModeSignIn}, "")
	for name, change := range map[string]func(*Adapter){
		"no path":         func(a *Adapter) { a.Path = "" },
		"no API":          func(a *Adapter) { a.API = 0 },
		"no mode":         func(a *Adapter) { a.Modes = nil },
		"unknown mode":    func(a *Adapter) { a.Modes = []Mode{3} },
		"no turn":         func(a *Adapter) { a.Turn = nil },
		"sign-in, no env": func(a *Adapter) { a.SignIn = nil },
	} {
		a := good
		change(&a)
		if err := a.validate(); err == nil {
			t.Errorf("%s: validate() = nil, want an error", name)
		}
	}
	if err := good.validate(); err != nil {
		t.Errorf("validate() = %v for a good adapter", err)
	}
}
