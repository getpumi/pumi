// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"getpumi.dev/pumi/api/events"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	"getpumi.dev/pumi/internal/kernel/firstworkflow"
	"getpumi.dev/pumi/internal/runner"
)

// The tests of the default role of the model configuration, which serves
// each model role that the workflow names and the configuration does not
// list (decisions 0256 and 0257), and of the maker that each invocation
// records (decision 0261).

// builtInRoles are the model roles of the built-in workflow (decision
// 0256).
var builtInRoles = []string{"auditor", "implementer", "plan-reviewer", "planner", "second-opinion"}

// defaultOnClaude is a default role on claude-code that falls back to
// codex.
const defaultOnClaude = `default_role { adapter: "claude-code" model: "claude-opus-5-5" fallback { adapter: "codex" model: "gpt-6.2-sol" } }`

// readWithBuiltInWorkflow reads the configuration file with models as its
// model configuration and gives it the roles of the built-in workflow.
func readWithBuiltInWorkflow(t *testing.T, models string) (*config, error) {
	t.Helper()
	cfg, err := readConfig(writeTestConfig(t, models))
	if err != nil {
		t.Fatalf("readConfig = %v, want the file read", err)
	}
	wf, err := firstworkflow.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg, resolveRoles(cfg, wf)
}

// TestOnlyTheDefaultServesEveryRoleOfTheBuiltInWorkflow: a configuration
// that lists only the default role runs every agent state of the built-in
// workflow on it, with its fallback (decision 0257).
func TestOnlyTheDefaultServesEveryRoleOfTheBuiltInWorkflow(t *testing.T) {
	cfg, err := readWithBuiltInWorkflow(t, models(signedIn, keyed, defaultOnClaude, checks))
	if err != nil {
		t.Fatalf("resolveRoles = %v, want the default to serve every role", err)
	}
	adapters := cfg.roleAdapters()
	if got := slices.Sorted(maps.Keys(adapters)); !slices.Equal(got, builtInRoles) {
		t.Fatalf("the roles are %q, want %q", got, builtInRoles)
	}
	for _, role := range builtInRoles {
		if adapters[role] != "claude-code" || roleModels(cfg)[role].Model != "claude-opus-5-5" {
			t.Errorf("the role %s is on %s with %+v, want the default, claude-opus-5-5 on claude-code", role, adapters[role], roleModels(cfg)[role])
		}
		if got := fallbacks(cfg)[role]; got != (runner.Fallback{Adapter: "codex", Model: "gpt-6.2-sol"}) {
			t.Errorf("the role %s falls back to %+v, want the default's fallback, gpt-6.2-sol on codex", role, got)
		}
	}
}

// TestTheRolesTheConfigurationListsAreNotTheDefault: a configuration that
// puts plan-reviewer and auditor on codex runs those two there and the
// other agent states on the default (decision 0257).
func TestTheRolesTheConfigurationListsAreNotTheDefault(t *testing.T) {
	onCodex := `roles { key: "plan-reviewer" value { adapter: "codex" model: "gpt-6.2-sol" } } ` +
		`roles { key: "auditor" value { adapter: "codex" model: "gpt-6.2-sol" } }`
	cfg, err := readWithBuiltInWorkflow(t, models(signedIn, keyed, defaultOnClaude, onCodex, checks))
	if err != nil {
		t.Fatalf("resolveRoles = %v, want the roles resolved", err)
	}
	want := map[string]string{
		"planner": "claude-code", "second-opinion": "claude-code", "implementer": "claude-code",
		"plan-reviewer": "codex", "auditor": "codex",
	}
	if got := cfg.roleAdapters(); !maps.Equal(got, want) {
		t.Errorf("the roles' adapters are %v, want %v", got, want)
	}
	if _, ok := fallbacks(cfg)["auditor"]; ok {
		t.Errorf("auditor has the default's fallback, want none: the configuration lists it without one")
	}
}

// TestARoleThatIsNotListedWithNoDefaultIsRefused: a configuration with
// neither a role that the workflow names nor a default is refused, with an
// error that names the missing roles and the default role (decision 0257).
func TestARoleThatIsNotListedWithNoDefaultIsRefused(t *testing.T) {
	only := `roles { key: "implementer" value { adapter: "claude-code" model: "claude-opus-5-5" } }`
	_, err := readWithBuiltInWorkflow(t, models(signedIn, keyed, only, checks))
	if err == nil {
		t.Fatal("resolveRoles = nil, want the missing roles refused")
	}
	for _, want := range []string{`"auditor"`, `"plan-reviewer"`, `"planner"`, `"second-opinion"`, "default_role"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), `"implementer"`) {
		t.Errorf("the error %q names implementer, which the configuration lists", err)
	}
}

// rolesModels returns a model configuration, in text format, with the test
// adapter in the API key mode, whose default role runs testModel falling
// back to testFallback on the same adapter, and the roles extra.
func rolesModels(t *testing.T, extra string) string {
	t.Helper()
	return adapterEntry(t, testAdapterName, "/proc/self/exe", apiKeyMode) + ` ` +
		fmt.Sprintf(`default_role { adapter: %q model: %q fallback { adapter: %q model: %q } }`, testAdapterName, testModel, testAdapterName, testFallback) +
		` ` + extra + ` check_adapter: "` + testAdapterName + `"`
}

// makersOf returns the maker of each invocation.result of evs, by
// invocation.
func makersOf(t *testing.T, evs []*eventsv1.InvocationResult) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, r := range evs {
		out[r.GetInvocation()] = r.GetMaker()
	}
	return out
}

// results returns the data of the invocation.result events of the
// clone's log.
func (c *commandClone) results() []*eventsv1.InvocationResult {
	c.t.Helper()
	var out []*eventsv1.InvocationResult
	for _, e := range ofType(c.log(), events.TypeInvocationResult) {
		out = append(out, dataOf(c.t, e, &eventsv1.InvocationResult{}))
	}
	return out
}

// TestADefaultAloneRunsEveryAgentState: the configuration lists only the
// default role, so the three agent states of the roles workflow,
// plan-reviewer, implementer and auditor, each run on its model, and each
// invocation records the model's maker, Anthropic (decisions 0257 and
// 0261).
func TestADefaultAloneRunsEveryAgentState(t *testing.T) {
	t.Setenv(testKeyEnv, testProviderKey)
	c := newCommandCloneWith(t, "roles", adapterSucceeds, rolesModels(t, ""))
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	r.stop(t)
	c.checkCalls(0, testModel, testModel, testModel)
	want := map[string]string{"a1/1": "Anthropic", "a1/2": "Anthropic", "a1/3": "Anthropic"}
	if got := makersOf(t, c.results()); !maps.Equal(got, want) {
		t.Errorf("the invocations' makers are %v, want %v", got, want)
	}
}

// TestPlanReviewerAndAuditorRunOnAnotherAdapter: plan-reviewer and auditor
// are on a second adapter in the sign-in mode, with an OpenAI model, and
// implementer uses the default on the test adapter. Each invocation runs
// on its role's adapter and records its model's maker (decisions 0245,
// 0257 and 0261).
func TestPlanReviewerAndAuditorRunOnAnotherAdapter(t *testing.T) {
	t.Setenv(testKeyEnv, testProviderKey)
	second := filepath.Join(t.TempDir(), "second-adapter")
	if err := os.Symlink("/proc/self/exe", second); err != nil {
		t.Fatal(err)
	}
	onSecond := adapterEntry(t, "second", second, signInMode) +
		` roles { key: "plan-reviewer" value { adapter: "second" model: "gpt-6.2-sol" } }` +
		` roles { key: "auditor" value { adapter: "second" model: "gpt-6.2-sol" } }`
	c := newCommandCloneWith(t, "roles", adapterSucceeds, rolesModels(t, onSecond))
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	r.stop(t)
	for invocation, adapter := range map[string]string{"a1/1": "second (" + second, "a1/2": testAdapterName + " (/proc/self/exe", "a1/3": "second (" + second} {
		if log := c.must("log", "--config", c.config, invocation); !strings.Contains(log, "the adapter "+adapter) {
			t.Errorf("the log of %s does not say that the adapter %s) ran it; it is\n%s", invocation, adapter, log)
		}
	}
	want := map[string]string{"a1/1": "OpenAI", "a1/2": "Anthropic", "a1/3": "OpenAI"}
	if got := makersOf(t, c.results()); !maps.Equal(got, want) {
		t.Errorf("the invocations' makers are %v, want %v", got, want)
	}
}
