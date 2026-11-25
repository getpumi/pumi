// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// modelsOf returns the model of each call that the provider received.
func modelsOf(t *testing.T, calls []providerCall) []string {
	t.Helper()
	var got []string
	for _, c := range calls {
		var m struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(c.body, &m); err != nil {
			t.Fatal(err)
		}
		got = append(got, m.Model)
	}
	return got
}

// TestRunSendsACallOfTheModelRoleToTheRolesModel: the harness invocation
// carries the state's model role, the test adapter asks for it by name,
// and the gateway sends the call to the model that the configuration file
// maps the role to (decision 0233).
func TestRunSendsACallOfTheModelRoleToTheRolesModel(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterSucceeds)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	r.stop(t)
	if got := modelsOf(t, c.provider.received()); !slices.Equal(got, []string{testModel}) {
		t.Errorf("the provider received calls to %q, want one to %s", got, testModel)
	}
}

// TestALimitedCallFallsBackAndTheLogSaysSo: the provider answers the
// role's model with a rate limit, the gateway sends the call once to the
// role's fallback model, the plan goes on, and the invocation's stored log
// holds the fallback (decision 0233).
func TestALimitedCallFallsBackAndTheLogSaysSo(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterSucceeds)
	c.provider.limited = map[string]bool{testModel: true}
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	r.stop(t)
	if got := modelsOf(t, c.provider.received()); !slices.Equal(got, []string{testModel, testFallback}) {
		t.Errorf("the provider received calls to %q, want one to %s and one to %s", got, testModel, testFallback)
	}
	log := c.must("log", "--config", c.config, "a1/1")
	if !strings.Contains(log, "pumi gateway: ") || !strings.Contains(log, "fell back") || !strings.Contains(log, testFallback) {
		t.Errorf("the log of a1/1 does not hold the fallback; it is\n%s", log)
	}
}

// TestRunRefusesAModelRoleTheConfigurationDoesNotMap: pumi run does not
// start when the workflow names a model role that the model configuration
// does not map, or when it maps a role to a model that the price table
// does not list (decision 0233).
func TestRunRefusesAModelRoleTheConfigurationDoesNotMap(t *testing.T) {
	adapter := adapterEntry(t, testAdapterName, "/proc/self/exe", signInMode) + ` check_adapter: "` + testAdapterName + `"`
	for name, tc := range map[string]struct{ models, want string }{
		"no roles":       {adapter, `"frontier"`},
		"another role":   {adapter + ` roles { key: "fast" value { adapter: "test" model: "claude-haiku-5-5" } }`, `"frontier"`},
		"unlisted model": {adapter + ` roles { key: "frontier" value { adapter: "test" model: "claude-unknown" } }`, `"claude-unknown"`},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCommandCloneWith(t, "tracks", adapterSucceeds, tc.models)
			ctx, cancel := context.WithTimeout(t.Context(), commandWait)
			defer cancel()
			var out bytes.Buffer
			err := execute(ctx, c.dir, []string{"run", "--config", c.config}, &out, &out)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("pumi run = %v; want an error that names %s; its output:\n%s", err, tc.want, out.String())
			}
		})
	}
}

// TestTheModelRolePicksTheAdapterOfItsAgentRun: the model role of the
// workflow's agent run, frontier, is on a second adapter, and the checks
// are on the test adapter; the invocation's log says that the second
// adapter ran it, with its SHA-256 (decision 0245).
func TestTheModelRolePicksTheAdapterOfItsAgentRun(t *testing.T) {
	second := filepath.Join(t.TempDir(), "second-adapter")
	if err := os.Symlink("/proc/self/exe", second); err != nil {
		t.Fatal(err)
	}
	models := adapterEntry(t, testAdapterName, "/proc/self/exe", signInMode) + ` ` +
		adapterEntry(t, "second", second, signInMode) + ` ` + testRole("second") + ` check_adapter: "` + testAdapterName + `"`
	c := newCommandCloneWith(t, "tracks", adapterSucceeds, models)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	r.stop(t)
	log := c.must("log", "--config", c.config, "a1/1")
	if want := "the adapter second (" + second + ", SHA-256 " + testAdapterHash(t) + ")"; !strings.Contains(log, want) {
		t.Errorf("the log of a1/1 does not hold %q; it is\n%s", want, log)
	}
}

// TestAnAdapterWhoseHashDiffersFailsItsInvocation: the configuration pins
// another SHA-256 for the adapter, so the runner does not start it, and
// the invocation's log names the adapter and both hashes (decision 0245).
func TestAnAdapterWhoseHashDiffersFailsItsInvocation(t *testing.T) {
	pinned := strings.Repeat("ab", 32)
	models := strings.Replace(testModels(t, signInMode), testAdapterHash(t), pinned, 1)
	c := newCommandCloneWith(t, "tracks", adapterSucceeds, models)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "implementing-escalated")
	r.stop(t)
	log := c.must("log", "--config", c.config, "a1/1")
	for _, want := range []string{"the adapter " + testAdapterName, pinned, testAdapterHash(t)} {
		if !strings.Contains(log, want) {
			t.Errorf("the log of a1/1 does not hold %q; it is\n%s", want, log)
		}
	}
	if strings.Contains(log, testAdapterPrefix) {
		t.Errorf("the adapter started; the log is\n%s", log)
	}
}
