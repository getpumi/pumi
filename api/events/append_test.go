// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package events

import (
	"slices"
	"testing"
)

// allAppenders are the appenders of decision 0156.
var allAppenders = []Appender{
	AppenderKernel,
	AppenderPerson,
	AppenderControlPlane,
	AppenderRunnerClaim,
	AppenderRunnerReportAgentRole,
	AppenderRunnerReportGateRole,
	AppenderRunnerReportExternalAction,
}

// everyReport is a runner's Report for an invocation of any kind and role.
var everyReport = []Appender{
	AppenderRunnerReportAgentRole,
	AppenderRunnerReportGateRole,
	AppenderRunnerReportExternalAction,
}

// decision0156 is the table of decision 0156, written out apart from
// append.go.
var decision0156 = map[string][]Appender{
	TypePlanAdded:           {AppenderPerson},
	TypeTransitionFired:     {AppenderKernel},
	TypeInvocationRequested: {AppenderKernel},
	TypeEscalationRaised:    {AppenderKernel},
	// The kernel cancels when a budget or policy stops a run.
	TypeCancelRequested:    {AppenderKernel, AppenderPerson},
	TypeApprovalRecorded:   {AppenderPerson},
	TypeOverrideRecorded:   {AppenderPerson},
	TypeEscalationResolved: {AppenderPerson},
	TypeWarningReviewed:    {AppenderPerson},
	TypeInvocationStarted:  {AppenderRunnerClaim},
	TypeInvocationResult:   everyReport,
	// The kernel resolves an external action in doubt that happened
	// (decision 0202).
	TypeInvocationFinished: append([]Appender{AppenderKernel}, everyReport...),
	TypeVerdictRecorded:    {AppenderRunnerReportGateRole},
	TypeWarningRaised:      {AppenderRunnerReportGateRole},
	TypeMergePerformed:     {AppenderRunnerReportExternalAction},
	TypeCheckIngested:      {AppenderRunnerReportExternalAction},
	TypeTrunkUnattested:    {AppenderRunnerReportExternalAction},
	TypeDecisionMade:       {AppenderControlPlane},
	TypeLeaseGranted:       {AppenderControlPlane},
	TypeLeaseReleased:      {AppenderControlPlane},
	TypeLeaseExpired:       {AppenderControlPlane},
}

func TestAppendTableListsEveryTypeAndNoOther(t *testing.T) {
	for _, typ := range Types() {
		if len(appendTable[typ]) == 0 {
			t.Errorf("the append table has no appender for %s", typ)
		}
	}
	for typ := range appendTable {
		if _, err := Data(typ); err != nil {
			t.Errorf("the append table names %q, which is not an event type: %v", typ, err)
		}
	}
}

func TestAppendTableNamesOnlyKnownAppenders(t *testing.T) {
	for typ, appenders := range appendTable {
		for _, a := range appenders {
			if !slices.Contains(allAppenders, a) {
				t.Errorf("the append table names %q for %s, which is not an appender of decision 0156", a, typ)
			}
		}
		if sorted := slices.Compact(slices.Sorted(slices.Values(appenders))); len(sorted) != len(appenders) {
			t.Errorf("the append table names an appender more than once for %s: %q", typ, appenders)
		}
	}
}

func TestAppendTableIsDecision0156(t *testing.T) {
	if len(decision0156) != len(Types()) {
		t.Fatalf("the test's table has %d types, want %d", len(decision0156), len(Types()))
	}
	for typ, want := range decision0156 {
		want = slices.Sorted(slices.Values(want))
		if got := Appenders(typ); !slices.Equal(got, want) {
			t.Errorf("Appenders(%s) = %q, want %q", typ, got, want)
		}
	}
}

func TestMayAppend(t *testing.T) {
	for _, typ := range Types() {
		for _, a := range allAppenders {
			want := slices.Contains(decision0156[typ], a)
			if got := MayAppend(typ, a); got != want {
				t.Errorf("MayAppend(%s, %q) = %v, want %v", typ, a, got, want)
			}
		}
	}
	if MayAppend("dev.getpumi.no.such.type", AppenderKernel) {
		t.Error("MayAppend allows a type that does not exist")
	}
}

func TestAnAgentRoleReportWritesOnlyItsResult(t *testing.T) {
	got := []string{}
	for _, typ := range Types() {
		if MayAppend(typ, AppenderRunnerReportAgentRole) {
			got = append(got, typ)
		}
	}
	want := []string{TypeInvocationFinished, TypeInvocationResult}
	if !slices.Equal(got, want) {
		t.Errorf("an agent-role Report may append %q, want only %q", got, want)
	}
}

func TestAppendersReturnsACopy(t *testing.T) {
	got := Appenders(TypeCancelRequested)
	if len(got) == 0 {
		t.Fatal("Appenders(cancel.requested) is empty")
	}
	got[0] = "changed"
	if slices.Contains(Appenders(TypeCancelRequested), "changed") {
		t.Error("changing the result of Appenders changed the table")
	}
}
