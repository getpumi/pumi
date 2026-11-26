// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package kernel

import (
	"testing"

	"getpumi.dev/pumi/api/events"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
)

// TestACheckNamesItsComponent: the invocation document of a check names
// the check's component from the workflow, such as spec, by which the
// configuration names its program, while an agent run keeps the
// component of the base (decision 0272).
func TestACheckNamesItsComponent(t *testing.T) {
	k, r := firstWorkflowKernel(t, Project{}, treeFiles{})
	for state, want := range map[string]string{"spec": "spec", "checking": "checks", "planning": "scripted"} {
		req, err := r.request(t.Context(), state, k.states[state].GetInvoke(), "cause")
		if err != nil {
			t.Fatal(err)
		}
		var data eventsv1.InvocationRequested
		if err := unpack(req, &data); err != nil {
			t.Fatal(err)
		}
		doc, err := events.Document(&data)
		if err != nil {
			t.Fatal(err)
		}
		if got := doc.GetComponent(); got.GetName() != want || (state != "planning" && got.GetVersion() != "") {
			t.Errorf("the component of the invocation of %s is %v, want the name %s, and no version for a check, whose version is its program's", state, got, want)
		}
	}
	if got := k.cfg.Base.GetComponent().GetName(); got != "scripted" {
		t.Errorf("a check changed the base's component to %q", got)
	}
}
