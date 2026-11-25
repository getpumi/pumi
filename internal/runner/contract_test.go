// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"strings"
	"testing"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"google.golang.org/protobuf/proto"
)

// usageNote is the start of the line that the runner writes to the log
// for each usage figure that the adapter passes.
const usageNote = runnerPrefix + "the adapter passed the usage of a call"

func TestALimitedRunFailsWithTheLimitAsItsReason(t *testing.T) {
	f := newFixture(t, "limited")
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_FAILED {
		t.Errorf("the outcome is %v, want failed; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if want := "the provider's usage or rate limit stopped the run"; !strings.Contains(f.service.log(), want) {
		t.Errorf("the log does not say %q (decision 0249); it is\n%s", want, f.service.log())
	}
	if got := report.GetResult().GetRefusals(); !refusalsEqual(got) {
		t.Errorf("the result's refusals are %v, want the adapter's", got)
	}
}

func TestTheRefusalsGoIntoTheResult(t *testing.T) {
	f := newFixture(t, "refusals")
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Errorf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	if got := report.GetResult().GetRefusals(); !refusalsEqual(got) {
		t.Errorf("the result's refusals are %v, want the adapter's (decision 0249)", got)
	}
	for _, want := range []string{"README.md", "GOFLAGS=-x go test ./...", "example.com"} {
		if !strings.Contains(f.service.log(), runnerPrefix+"the harness refused") || !strings.Contains(f.service.log(), want) {
			t.Errorf("the log does not name the refusal of %q; it is\n%s", want, f.service.log())
		}
	}
}

func TestUsageFiguresAreReadBeforeTheResult(t *testing.T) {
	f := newFixture(t, "usage then stop")
	// The cancel comes only once the runner logged the figure, and the
	// adapter writes its result only after the cancel closes its standard
	// input.
	out := &syncBuffer{}
	f.cfg.Output = out
	f.service.cancelWhen = func() bool { return strings.Contains(out.String(), usageNote) }
	// A runner that never reads the figure stops the adapter at this
	// limit, and the outcome is then cap reached.
	f.doc.Budget.Seconds = 20
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_CANCELLED {
		t.Errorf("the outcome is %v, want cancelled; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	want := usageNote + " to claude-sonnet-5-5: 3 input, 5 five-minute cache write, 7 one-hour cache write, 11 cache read and 13 output tokens, 2 web searches, speed fast, inference geography us"
	if !strings.Contains(f.service.log(), want) {
		t.Errorf("the log does not hold %q (decision 0241); it is\n%s", want, f.service.log())
	}
	if u := report.GetResult().GetUsage(); u.GetTokens() != gatewayTokens {
		t.Errorf("the usage is %v, want the gateway's totals (decision 0230)", u)
	}
}

// refusalsEqual reports whether got are the fake adapter's refusals.
func refusalsEqual(got []*harnessv1.Refusal) bool {
	want := testRefusals()
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !proto.Equal(got[i], want[i]) {
			return false
		}
	}
	return true
}
