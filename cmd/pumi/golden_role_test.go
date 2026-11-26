// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
)

// TestTheInvocationDocumentCarriesTheStatesModelRole: the spike's
// implementing state starts a harness with the model role implementer, and
// the kernel writes the role into the invocation document (decision
// 0233).
func TestTheInvocationDocumentCarriesTheStatesModelRole(t *testing.T) {
	p := cancelPumi(t, "spike", caseSettings{})
	p.doSteps(addPlanA)
	requested := kernelEvents(p.events(), events.TypeInvocationRequested, casePlan)
	if len(requested) != 1 {
		t.Fatalf("the kernel requested %d invocations, want 1", len(requested))
	}
	doc := &runnerv1.Invocation{}
	if err := proto.Unmarshal(dataOf(t, requested[0], &eventsv1.InvocationRequested{}).GetDocument(), doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.GetModelRole(); got != "implementer" {
		t.Errorf("the document's model role = %q, want implementer", got)
	}
}
