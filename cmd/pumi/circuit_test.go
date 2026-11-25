// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// The tests of the circuit breaker: a model role whose run the provider's
// limit stops falls back to another adapter, and stays there until a
// person switches it back (decision 0246).

// adapterStopsAtALimit makes one model call, and reports the status
// limited when the provider answers it with a rate limit, as an adapter
// does when the limit stops its harness (decision 0249), and completes
// otherwise.
const adapterStopsAtALimit = "stops at a limit"

// fallbackAdapterName is the name of the second test adapter, on which
// the model role falls back.
const fallbackAdapterName = "test-fallback"

// stopsAtALimit acts as the test adapter in the mode adapterStopsAtALimit
// and returns its exit status.
func stopsAtALimit(inv *harnessv1.Invocation) int {
	status, err := callModel(inv, testOutputTokens)
	result := &harnessv1.Result{Status: harnessv1.Result_STATUS_COMPLETED, Output: `{"summary":"Implemented the plan."}`}
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, testAdapterPrefix+"the call failed:", err)
		return 9
	case status == http.StatusTooManyRequests:
		fmt.Fprintln(os.Stderr, testAdapterPrefix+"the provider's limit stopped the run")
		result = &harnessv1.Result{Status: harnessv1.Result_STATUS_LIMITED}
	case status != http.StatusOK:
		fmt.Fprintf(os.Stderr, "%sthe model call gave %d\n", testAdapterPrefix, status)
		return 9
	}
	out, err := protojson.Marshal(&harnessv1.Line{Kind: &harnessv1.Line_Result{Result: result}})
	if err != nil {
		panic(err)
	}
	fmt.Println(string(out))
	return 0
}

// circuitModels returns a model configuration, in text format, with two
// test adapters in the mode mode: the model role frontier runs on testModel
// of the adapter test, and falls back to testFallback on the adapter
// test-fallback, or has no fallback when noFallback is set.
func circuitModels(t *testing.T, mode string, noFallback bool) string {
	t.Helper()
	fallback := fmt.Sprintf(`fallback { adapter: %q model: %q }`, fallbackAdapterName, testFallback)
	if noFallback {
		fallback = ""
	}
	return adapterEntry(t, testAdapterName, "/proc/self/exe", mode) + ` ` +
		adapterEntry(t, fallbackAdapterName, "/proc/self/exe", mode) + ` ` +
		fmt.Sprintf(`roles { key: "frontier" value { adapter: %q model: %q %s } }`, testAdapterName, testModel, fallback) +
		` check_adapter: "` + testAdapterName + `"`
}

// newCircuitClone returns a clone with the configuration of circuitModels
// in the API key mode, whose test adapter stops at a limit, and whose
// provider limits the models limited.
func newCircuitClone(t *testing.T, noFallback bool, limited ...string) *commandClone {
	t.Helper()
	t.Setenv(testKeyEnv, testProviderKey)
	c := newCommandCloneWith(t, "tracks", adapterStopsAtALimit, circuitModels(t, apiKeyMode, noFallback))
	c.provider.limit(limited...)
	return c
}

// switchesOf returns the data of the model_role.switched events of evs.
func switchesOf(t *testing.T, evs []*cloudeventsv1.CloudEvent) []*eventsv1.ModelRoleSwitched {
	t.Helper()
	var out []*eventsv1.ModelRoleSwitched
	for _, e := range ofType(evs, events.TypeModelRoleSwitched) {
		s := &eventsv1.ModelRoleSwitched{}
		if err := e.GetProtoData().UnmarshalTo(s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// switchedByA1 is the switch of frontier to its fallback that the limit
// on the run of a1/1 caused.
var switchedByA1 = &eventsv1.ModelRoleSwitched{
	Role: "frontier", Adapter: fallbackAdapterName, Model: testFallback, Invocation: "a1/1",
	Limit: &eventsv1.ModelRoleSwitched_Limit{Adapter: testAdapterName, Model: testModel},
}

// switchedBack is the person's switch of frontier back to the adapter test.
var switchedBack = &eventsv1.ModelRoleSwitched{
	Role: "frontier", Adapter: testAdapterName, Model: testModel, SwitchedBy: testPerson,
	Limit: &eventsv1.ModelRoleSwitched_Limit{Adapter: testAdapterName, Model: testModel},
}

// checkSwitches fails the test unless the switches of the clone's log are
// want.
func (c *commandClone) checkSwitches(want ...*eventsv1.ModelRoleSwitched) {
	c.t.Helper()
	got := switchesOf(c.t, c.log())
	if !slices.EqualFunc(got, want, func(a, b *eventsv1.ModelRoleSwitched) bool { return proto.Equal(a, b) }) {
		c.t.Errorf("the log's switches are %v, want %v", got, want)
	}
}

// checkCalls fails the test unless the provider received calls to the
// models want, in order, since it received from calls.
func (c *commandClone) checkCalls(from int, want ...string) {
	c.t.Helper()
	if got := modelsOf(c.t, c.provider.received()[from:]); !slices.Equal(got, want) {
		c.t.Errorf("the provider received calls to %q, want %q", got, want)
	}
}

// TestTheCircuitBreaker: the provider's limit stops the run of frontier on
// the adapter test. The runner starts the fallback adapter once, which the
// gateway sends to the fallback's model, and the plan merges; the log
// holds the switch. The next plan runs on the fallback at once, also after
// pumi run starts again, and pumi status shows the role as switched.
// pumi role reset switches it back, and the plan after that runs on the
// role's own adapter and model again (decision 0246).
func TestTheCircuitBreaker(t *testing.T) {
	c := newCircuitClone(t, false, testModel)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	c.checkCalls(0, testModel, testFallback)
	c.checkSwitches(switchedByA1)
	log := c.must("log", "a1/1")
	for _, want := range []string{"the provider's limit stopped the run", "switches to its fallback", "the adapter " + fallbackAdapterName + " ("} {
		if !strings.Contains(log, want) {
			t.Errorf("the log of a1/1 does not hold %q; it is\n%s", want, log)
		}
	}

	c.must("add", "--track", "b", "b1")
	c.waitState(r, "b1", "merged")
	c.checkCalls(2, testFallback)
	r.stop(t)

	if out := c.must("status", "--config", c.config); !strings.Contains(out, "frontier") || !strings.Contains(out, fallbackAdapterName) || !strings.Contains(out, "a1/1") {
		t.Errorf("pumi status does not show frontier on its fallback since a1/1:\n%s", out)
	}
	r = c.start()
	c.must("add", "--track", "c", "c1")
	c.waitState(r, "c1", "merged")
	c.checkCalls(3, testFallback)

	c.provider.limit()
	if out := c.must("role", "reset", "frontier"); !strings.Contains(out, "frontier") {
		t.Errorf("pumi role reset wrote %q, want it to name the role", out)
	}
	c.checkSwitches(switchedByA1, switchedBack)
	if out := c.must("status"); strings.Contains(out, fallbackAdapterName) {
		t.Errorf("pumi status still shows frontier on its fallback:\n%s", out)
	}
	c.must("add", "--track", "d", "d1")
	c.waitState(r, "d1", "merged")
	c.checkCalls(4, testModel)
	c.checkSwitches(switchedByA1, switchedBack)
	r.stop(t)
}

// TestALimitOnTheFallbackFails: the provider limits both models. The
// fallback's run is stopped too, so a1/1 fails with the limit as its
// reason and the failed trigger runs it again as a1/2, which runs on the
// fallback at once and fails the same way; with no attempt left, the plan
// escalates. There is one switch (decisions 0235 and 0246).
func TestALimitOnTheFallbackFails(t *testing.T) {
	c := newCircuitClone(t, false, testModel, testFallback)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "implementing-escalated")
	r.stop(t)
	c.checkCalls(0, testModel, testFallback, testFallback)
	c.checkSwitches(switchedByA1)
	for _, e := range ofType(c.log(), events.TypeInvocationFinished) {
		if f := dataOf(t, e, &eventsv1.InvocationFinished{}); f.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
			t.Errorf("%s finished %v, want failed", f.GetInvocation(), f.GetOutcome())
		}
	}
}

// TestALimitOnARoleWithNoFallbackFails: a model role with no fallback does
// not switch; the run fails with the limit as its reason, runs again
// once, and the plan escalates (decisions 0235 and 0246).
func TestALimitOnARoleWithNoFallbackFails(t *testing.T) {
	c := newCircuitClone(t, true, testModel)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "implementing-escalated")
	r.stop(t)
	c.checkCalls(0, testModel, testModel)
	c.checkSwitches()
	if log := c.must("log", "--config", c.config, "a1/1"); !strings.Contains(log, "the provider's usage or rate limit stopped the run") {
		t.Errorf("the log of a1/1 does not give the limit as the reason; it is\n%s", log)
	}
}

// TestRoleResetRefusesARoleThatIsNotSwitched: pumi role reset switches
// back only a model role that is on its fallback.
func TestRoleResetRefusesARoleThatIsNotSwitched(t *testing.T) {
	c := newCircuitClone(t, false)
	out, err := c.pumi("role", "reset", "--config", c.config, "frontier")
	if err == nil || !strings.Contains(err.Error(), "frontier") {
		t.Errorf("pumi role reset = %v, %q; want an error that names frontier", err, out)
	}
	c.checkSwitches()
}
