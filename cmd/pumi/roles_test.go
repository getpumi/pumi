// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"context"
	"encoding/json"
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
// start when the workflow names a model role that the configuration file
// does not map, or when the file maps a role to a model that the price
// table does not list (decision 0233).
func TestRunRefusesAModelRoleTheConfigurationDoesNotMap(t *testing.T) {
	for name, tc := range map[string]struct{ gateway, want string }{
		"no roles":       {"", `"frontier"`},
		"another role":   {`model_roles { key: "fast" value { model: "claude-haiku-5-5" fallback: "claude-sonnet-5-5" } }`, `"frontier"`},
		"unlisted model": {`model_roles { key: "frontier" value { model: "claude-unknown" fallback: "claude-sonnet-5-5" } }`, `"claude-unknown"`},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCommandCloneWith(t, "tracks", adapterSucceeds, tc.gateway)
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
