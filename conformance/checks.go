// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// planned is one step of a scenario with what the suite expects of it.
type planned struct {
	step Step
	// refused says that the capability list leaves the step out.
	refused bool
	// target is the file that shows that the step ran: the file a write
	// writes or a command makes, relative to the workspace or absolute;
	// empty for none.
	target string
	// refusal is the refusal that the result names when the adapter says
	// that its harness reports refusals; nil when the scenario does not
	// require one, as for a kind of tool that the harness does not offer.
	refusal *harnessv1.Refusal
}

// checkCalls checks every model call: the harness made at least one that
// carries the task; each names the model role on the API key path, or the
// role's model or its fallback on the sign-in path (decisions 0233 and
// 0254); each carries the invocation token on the API key path, and never
// the person's sign-in there, or the sign-in on the sign-in path
// (decisions 0225 and 0240). No call reaches where the person's settings
// point in the API key mode.
func checkCalls(t T, w *world) {
	t.Helper()
	calls := w.provider.taken()
	if !slices.ContainsFunc(calls, func(c call) bool { return c.scripted }) {
		t.Errorf("the harness made no call to the fake provider that carries the task; it took %d calls", len(calls))
	}
	problems := map[string]bool{}
	for _, c := range calls {
		if c.endpoint == notAModelCall {
			continue
		}
		for _, p := range w.callProblems(c) {
			problems[p] = true
		}
	}
	for _, p := range sortedKeys(problems) {
		t.Errorf("%s", p)
	}
	if paths := w.elsewhere.requested(); w.mode == ModeAPIKey && len(paths) > 0 {
		t.Errorf("the harness sent calls to %q where the person's own settings point, not to the model gateway (decisions 0226 and 0240)", paths)
	}
}

// callProblems returns what is wrong with the model call c.
func (w *world) callProblems(c call) []string {
	var out []string
	where := c.method + " " + c.path
	if w.mode == ModeAPIKey {
		if c.model != modelRole {
			out = append(out, fmt.Sprintf("a call to %s names the model %q, want the model role %q, which the gateway maps (decision 0233)", where, c.model, modelRole))
		}
		if !carriesToken(c.header, w.token) {
			out = append(out, fmt.Sprintf("a call to %s does not carry the invocation token in X-Pumi-Token, in X-Api-Key or as a bearer token (decision 0225)", where))
		}
		if carries(c.header, w.credential) {
			out = append(out, fmt.Sprintf("a call to %s carries the person's own sign-in, which the API key mode leaves out (decision 0240)", where))
		}
		return out
	}
	if c.model != model && c.model != fallbackModel {
		out = append(out, fmt.Sprintf("a call to %s names the model %q, want the role's model %q or its fallback %q (decision 0254)", where, c.model, model, fallbackModel))
	}
	if !carries(c.header, w.credential) {
		out = append(out, fmt.Sprintf("a call to %s does not carry the person's own sign-in (decision 0240)", where))
	}
	return out
}

// carriesToken reports whether a call with header carries token where the
// gateway reads it.
func carriesToken(h http.Header, token string) bool {
	return h.Get("X-Pumi-Token") == token || h.Get("X-Api-Key") == token || h.Get("Authorization") == "Bearer "+token
}

// carries reports whether any header holds s.
func carries(h http.Header, s string) bool {
	for _, values := range h {
		for _, v := range values {
			if strings.Contains(v, s) {
				return true
			}
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// checkPlan checks what each step did: a step that the list allows did
// what it asked, and one that it leaves out did not, with the refusal in
// the result when the adapter says that its harness reports refusals
// (decisions 0159 and 0249).
func checkPlan(t T, w *world, plan []planned, o *outcome) {
	t.Helper()
	for _, p := range plan {
		if p.target != "" {
			w.checkTarget(t, p)
		}
		if p.refused && p.step.Kind == StepRead {
			w.checkNotRead(t)
		}
		if p.refused && p.step.Kind == StepFetch {
			w.checkNotFetched(t, p.step)
		}
		if p.refusal != nil && w.a.ReportsRefusals && o.result != nil && !slices.ContainsFunc(o.result.GetRefusals(), w.matches(p.refusal)) {
			t.Errorf("the result's refusals %v do not name %v of the %v step, which the capability list leaves out; the adapter says that its harness reports refusals (decision 0249)", o.result.GetRefusals(), p.refusal, p.step.Kind)
		}
	}
}

// path returns the absolute path of a path relative to the workspace or
// absolute.
func (w *world) path(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(w.workspace, filepath.FromSlash(p))
}

func (w *world) checkTarget(t T, p planned) {
	t.Helper()
	_, err := os.Lstat(w.path(p.target))
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("conformance: %v", err)
		return
	}
	what := describe(p.step)
	switch {
	case p.refused && exists:
		t.Errorf("the harness did %s, which the capability list leaves out: %s exists", what, p.target)
	case !p.refused && !exists:
		t.Errorf("the harness did not do %s, which the capability list allows: %s does not exist", what, p.target)
	case !p.refused && p.step.Kind == StepWrite:
		if data, err := os.ReadFile(w.path(p.target)); err != nil || string(data) != p.step.Content {
			t.Errorf("the harness wrote %q to %s (%v), want %q", data, p.target, err, p.step.Content)
		}
	}
}

// checkNotRead checks that no call passed the secret to the model.
func (w *world) checkNotRead(t T) {
	t.Helper()
	for _, c := range w.provider.taken() {
		if strings.Contains(c.body, w.secret) {
			t.Errorf("a call to %s %s passed the content of %s to the model, although the capability list leaves reading out", c.method, c.path, secretFile)
			return
		}
	}
}

// checkNotFetched checks that the fetch of step did not reach its host.
func (w *world) checkNotFetched(t T, step Step) {
	t.Helper()
	u, err := url.Parse(step.URL)
	if err != nil {
		t.Errorf("conformance: %v", err)
		return
	}
	if slices.Contains(w.host.requested(), u.Path) {
		t.Errorf("the harness fetched %s, which the capability list leaves out", step.URL)
	}
}

// matches returns whether a refusal of the result is want: the same kind
// of tool, and the same path, relative to the workspace or absolute, the
// same command or the same host.
func (w *world) matches(want *harnessv1.Refusal) func(*harnessv1.Refusal) bool {
	return func(got *harnessv1.Refusal) bool {
		if got.GetTool() != want.GetTool() {
			return false
		}
		switch a := want.GetAsked().(type) {
		case *harnessv1.Refusal_Path:
			return got.GetPath() != "" && w.path(got.GetPath()) == w.path(a.Path)
		case *harnessv1.Refusal_Command:
			return strings.TrimSpace(got.GetCommand()) == a.Command
		case *harnessv1.Refusal_Host:
			return got.GetHost() == a.Host
		}
		return false
	}
}

// describe says what a step asked, for a message.
func describe(s Step) string {
	switch s.Kind {
	case StepWrite:
		return "the write of " + s.Path
	case StepRead:
		return "the read of " + s.Path
	case StepRun:
		return "the command " + s.Command
	case StepFetch:
		return "the fetch of " + s.URL
	case StepOutput:
		return "the structured output"
	}
	return s.Kind.String()
}
