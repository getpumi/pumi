// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"errors"
	"strings"
	"testing"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
)

// errBlobStore is the error of the failing blob store.
var errBlobStore = errors.New("the disk is full")

// sentBatches returns the log batches sent so far.
func (f *fakeService) sentBatches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.batches...)
}

// TestACredentialInOneBatchIsMasked: the fake adapter logs the harness
// invocation, which holds the gateway token. Neither the batches nor what
// pumi run prints hold it (decision 0232).
func TestACredentialInOneBatchIsMasked(t *testing.T) {
	f := newFixture(t, "success")
	var out syncBuffer
	f.cfg.Output = &out
	f.runOne(t)
	log := f.service.log()
	if strings.Contains(log, testToken) {
		t.Errorf("the log holds the gateway token; it is\n%s", log)
	}
	if !strings.Contains(log, maskedCredential) {
		t.Errorf("the log does not hold %q in place of the token; it is\n%s", maskedCredential, log)
	}
	if got := out.String(); strings.Contains(got, testToken) || !strings.Contains(got, maskedCredential) {
		t.Errorf("the output holds the gateway token, or not %q in its place; it is\n%s", maskedCredential, got)
	}
}

// TestACredentialSplitAcrossTwoBatchesIsMasked: the fake adapter writes
// the first half of the token, waits for several log batches and writes
// the second half. No batch holds either half (decision 0232).
func TestACredentialSplitAcrossTwoBatchesIsMasked(t *testing.T) {
	f := newFixture(t, "split credential")
	var out syncBuffer
	f.cfg.Output = &out
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
		t.Fatalf("the outcome is %v, want succeeded; the log is\n%s", report.GetOutcome(), f.service.log())
	}
	half := testToken[:len(testToken)/2]
	batches := f.service.sentBatches()
	if len(batches) < 2 {
		t.Fatalf("the runner sent %d log batches, want at least 2", len(batches))
	}
	for i, b := range batches {
		if strings.Contains(b, half) || strings.Contains(b, testToken[len(half):]) {
			t.Errorf("batch %d holds half of the gateway token: %q", i, b)
		}
	}
	if want := fakeAdapterPrefix + "token " + maskedCredential + " end\n"; !strings.Contains(f.service.log(), want) {
		t.Errorf("the log does not hold %q; it is\n%s", want, f.service.log())
	}
	if got := out.String(); strings.Contains(got, half) {
		t.Errorf("the output holds half of the gateway token; it is\n%s", got)
	}
}

// TestTheFullLogIsStored: the runner stores the full masked log in the
// blob store and records its hash in the result, also when the invocation
// failed or reached a cap (decisions 0034 and 0232).
func TestTheFullLogIsStored(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		limit   int64
		outcome eventsv1.InvocationFinished_Outcome
	}{
		{"success", 0, eventsv1.InvocationFinished_OUTCOME_SUCCEEDED},
		{"failed", 0, eventsv1.InvocationFinished_OUTCOME_FAILED},
		{"not json", 0, eventsv1.InvocationFinished_OUTCOME_FAILED},
		{"stop", 1, eventsv1.InvocationFinished_OUTCOME_CAP_REACHED},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newFixture(t, tc.mode)
			if tc.limit > 0 {
				f.withLimit(tc.limit)
			}
			report := f.runOne(t)
			if report.GetOutcome() != tc.outcome {
				t.Fatalf("the outcome is %v, want %v; the log is\n%s", report.GetOutcome(), tc.outcome, f.service.log())
			}
			f.checkStoredLog(t, report)
		})
	}
}

// checkStoredLog fails the test unless the blob store holds the masked log
// that the runner sent, under the hash that the report records.
func (f *fixture) checkStoredLog(t *testing.T, report *runnerv1.ReportRequest) {
	t.Helper()
	log := f.service.log()
	hash := report.GetResult().GetLogBlobHash()
	if hash != blobHash(log) {
		t.Errorf("the result's log blob hash is %q, want the hash of the log sent, %q", hash, blobHash(log))
	}
	stored, ok := f.blobs.stored()[hash]
	if !ok {
		t.Fatalf("the blob store holds no blob %q; it holds %v", hash, f.blobs.stored())
	}
	if stored != log || strings.Contains(stored, testToken) {
		t.Errorf("the stored log is not the masked log that was sent:\n%s\nwant\n%s", stored, log)
	}
}

// TestAnInvocationThatFailsBeforeTheAdapterStartsStoresItsLog: the log of
// an invocation whose input tree the repository does not have holds the
// runner's reason, and is stored.
func TestAnInvocationThatFailsBeforeTheAdapterStartsStoresItsLog(t *testing.T) {
	f := newFixture(t, "success")
	f.service.claim.Invocation.Input.Tree = "0123456789abcdef0123456789abcdef01234567"
	report := f.runOne(t)
	if !strings.Contains(f.service.log(), runnerPrefix) {
		t.Errorf("the log does not give the runner's reason; it is\n%s", f.service.log())
	}
	f.checkStoredLog(t, report)
}

// TestAFailedLogUploadKeepsTheOutcome: when the blob store fails, the
// result has no log hash, the log says why and the outcome stands.
func TestAFailedLogUploadKeepsTheOutcome(t *testing.T) {
	f := newFixture(t, "unchanged")
	f.blobs.err = errBlobStore
	report := f.runOne(t)
	if report.GetOutcome() != eventsv1.InvocationFinished_OUTCOME_SUCCEEDED || report.GetResult().GetLogBlobHash() != "" {
		t.Errorf("the outcome is %v with the log blob hash %q, want succeeded with none",
			report.GetOutcome(), report.GetResult().GetLogBlobHash())
	}
	if !strings.Contains(f.service.log(), runnerPrefix+"the log of plan-1/1 could not be stored") {
		t.Errorf("the log does not say that it could not be stored; it is\n%s", f.service.log())
	}
}

// TestTheMasker covers the masking of the log buffer directly: values
// that overlap, a value at the end of the log and empty values.
func TestTheMasker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		secrets []string
		writes  []string
		want    string
	}{
		{"none", nil, []string{"plain"}, "plain"},
		{"empty value", []string{""}, []string{"plain"}, "plain"},
		{"longest first", []string{"abc", "abcdef"}, []string{"x abcdef y abc"}, "x " + maskedCredential + " y " + maskedCredential},
		{"split", []string{"secret"}, []string{"a sec", "ret b"}, "a " + maskedCredential + " b"},
		{"split by bytes", []string{"secret"}, []string{"s", "e", "c", "r", "e", "t"}, maskedCredential},
		{"prefix at the end", []string{"secret"}, []string{"a secr"}, "a secr"},
		{"repeated prefix", []string{"aab"}, []string{"aa", "aab"}, "aa" + maskedCredential},
		{"twice", []string{"k1"}, []string{"k1k1"}, maskedCredential + maskedCredential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &logBuffer{}
			l.mask(tc.secrets...)
			var sent strings.Builder
			for _, w := range tc.writes {
				_, _ = l.Write([]byte(w))
				sent.Write(l.take())
			}
			l.end()
			sent.Write(l.take())
			if got := sent.String(); got != tc.want {
				t.Errorf("the batches give %q, want %q", got, tc.want)
			}
			if got := string(l.full()); got != tc.want {
				t.Errorf("the full log is %q, want %q", got, tc.want)
			}
		})
	}
}
