// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"getpumi.dev/pumi/internal/gateway"
)

// The model role of the tests, its model and its fallback model, which the
// price table lists.
const (
	role     = "frontier"
	model    = "claude-opus-5-5"
	fallback = "claude-sonnet-5-5"
	// retryWait is the wait before a retry that the tests configure.
	retryWait = 7 * time.Second
)

// The provider's errors, in the form of Anthropic's API.
const (
	rateLimited = `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit."}}`
	overloaded  = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	unavailable = `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`
)

// timeInLog is the form of the time in each line the gateway writes to the
// invocation's log.
var timeInLog = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ`)

// rolesFixture returns a fixture whose configuration maps role to model
// and fallback, and whose waits before a retry return at once and are
// recorded.
func rolesFixture(t *testing.T) (*fixture, *waits) {
	t.Helper()
	f := newFixtureWith(t, gateway.Config{
		Roles:     map[string]gateway.Role{role: {Model: model, Fallback: fallback}},
		RetryWait: retryWait,
	})
	w := &waits{}
	gateway.SetWait(f.gw, w.wait)
	return f, w
}

// waits records the gateway's waits, and runs during, when set, in each.
type waits struct {
	mu     sync.Mutex
	got    []time.Duration
	during func()
}

func (w *waits) wait(_ context.Context, d time.Duration) error {
	w.mu.Lock()
	w.got = append(w.got, d)
	during := w.during
	w.mu.Unlock()
	if during != nil {
		during()
	}
	return nil
}

func (w *waits) durations() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.got...)
}

// modelOf returns the model that a request body names.
func modelOf(t *testing.T, body []byte) string {
	t.Helper()
	var m struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("the request body %q: %v", body, err)
	}
	return m.Model
}

// models returns the model of each request that the provider received.
func (f *fixture) models(t *testing.T) []string {
	t.Helper()
	var got []string
	for _, r := range f.provider.received() {
		got = append(got, modelOf(t, r.body))
	}
	return got
}

// respondInTurn answers the nth request with the nth response, a status
// and a body; a status of 200 is answered as answer does.
func (f *fixture) respondInTurn(responses ...response) {
	var mu sync.Mutex
	n := 0
	f.provider.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		mu.Lock()
		resp := responses[min(n, len(responses)-1)]
		n++
		mu.Unlock()
		if resp.status == http.StatusOK {
			answer(w, r, body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = io.WriteString(w, resp.body)
	}
}

// response is one answer of respondInTurn.
type response struct {
	status int
	body   string
}

var ok = response{http.StatusOK, ""}

// equalStrings reports whether a and b hold the same strings in order.
func equalStrings(a, b []string) bool { return slices.Equal(a, b) }

// logLines returns the lines of the invocation's log that hold all of
// parts.
func (f *fixture) logLines(parts ...string) []string {
	var got []string
	for line := range strings.Lines(f.invocationLog.String()) {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(line, p)
		}
		if all {
			got = append(got, line)
		}
	}
	return got
}

func TestACallThatNamesARoleGoesToTheRolesModel(t *testing.T) {
	f, _ := rolesFixture(t)
	token := f.open(t, nil)

	status, _, _ := f.call(t, "/v1/messages", signInHeader(token), messages(role, false))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	got := f.provider.received()
	want := messages(model, false)
	if len(got) != 1 || string(got[0].body) != want {
		t.Fatalf("the provider received %d requests, the first %q, want one: %q", len(got), got[0].body, want)
	}
	if got := f.totals(t, token); got.Tokens != nonStreamedTokens {
		t.Errorf("Totals() = %+v, want %d tokens", got, nonStreamedTokens)
	}
}

func TestACompressedCallThatNamesARoleGoesToTheRolesModel(t *testing.T) {
	f, _ := rolesFixture(t)
	token := f.open(t, nil)
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, _ = io.WriteString(zw, messages(role, true))
	_ = zw.Close()
	h := signInHeader(token)
	h.Set("Content-Encoding", "gzip")

	status, _, _ := f.callBody(t, "/v1/messages", h, &zipped)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	got := f.provider.received()
	if len(got) != 1 || string(got[0].body) != messages(model, true) || got[0].header.Get("Content-Encoding") != "" {
		t.Errorf("the provider received %q with Content-Encoding %q, want the body uncompressed with the role's model",
			got[0].body, got[0].header.Get("Content-Encoding"))
	}
}

func TestACallThatNamesAModelGoesToThatModel(t *testing.T) {
	f, _ := rolesFixture(t)
	token := f.open(t, nil)

	status, _, _ := f.call(t, "/v1/messages", signInHeader(token), messages("claude-haiku-5-5", false))

	got := f.provider.received()
	if status != http.StatusOK || len(got) != 1 || string(got[0].body) != messages("claude-haiku-5-5", false) {
		t.Errorf("status = %d and the provider received %d requests, want 200 and the call unchanged", status, len(got))
	}
}

func TestNewRefusesARoleItCannotUse(t *testing.T) {
	for name, r := range map[string]gateway.Role{
		"no model":                       {Fallback: fallback},
		"a model the table lacks":        {Model: "claude-unknown", Fallback: fallback},
		"a fallback the table lacks":     {Model: model, Fallback: "gpt-unknown"},
		"the fallback is the same model": {Model: model, Fallback: model},
	} {
		t.Run(name, func(t *testing.T) {
			upstream, err := url.Parse("https://api.anthropic.com")
			if err != nil {
				t.Fatal(err)
			}
			_, err = gateway.New(gateway.Config{Upstreams: map[gateway.API]*url.URL{gateway.MessagesAPI: upstream}, Roles: map[string]gateway.Role{role: r}})
			if err == nil || !strings.Contains(err.Error(), `"frontier"`) {
				t.Errorf("New() error = %v, want one that names the role", err)
			}
		})
	}
}

func TestAUsageOrRateLimitFallsBackOnceToTheRolesFallback(t *testing.T) {
	f, w := rolesFixture(t)
	f.respondInTurn(response{http.StatusTooManyRequests, rateLimited}, ok)
	token := f.open(t, nil)

	status, _, body := f.call(t, "/v1/messages", signInHeader(token), messages(role, false))

	if status != http.StatusOK || body != nonStreamed {
		t.Errorf("response = %d %q, want the fallback's answer", status, body)
	}
	if got := f.models(t); !equalStrings(got, []string{model, fallback}) {
		t.Errorf("the provider received calls to %q, want %q and then %q", got, model, fallback)
	}
	if got := w.durations(); len(got) != 0 {
		t.Errorf("the gateway waited %v before the fallback, want no wait", got)
	}
	lines := f.logLines("fell back", role, model, fallback, "429")
	if len(lines) != 1 || !timeInLog.MatchString(lines[0]) {
		t.Errorf("the invocation's log = %q, want one line with the role, both models, the status and the time", f.invocationLog.String())
	}
	if got := f.totals(t, token); got.Tokens != nonStreamedTokens {
		t.Errorf("Totals() = %+v, want the fallback's %d tokens", got, nonStreamedTokens)
	}
}

func TestALimitOnBothModelsIsPassedBack(t *testing.T) {
	f, _ := rolesFixture(t)
	f.respondInTurn(response{http.StatusTooManyRequests, rateLimited})
	token := f.open(t, nil)

	status, _, body := f.call(t, "/v1/messages", signInHeader(token), messages(role, true))

	if status != http.StatusTooManyRequests || body != rateLimited {
		t.Errorf("response = %d %q, want the fallback's limit", status, body)
	}
	if got := f.models(t); !equalStrings(got, []string{model, fallback}) {
		t.Errorf("the provider received calls to %q, want one to each model", got)
	}
	if lines := f.logLines("limit", fallback, "429", "passed back"); len(lines) != 1 {
		t.Errorf("the invocation's log = %q, want a line that the fallback's limit is passed back", f.invocationLog.String())
	}
}

func TestALimitOnACallThatNamesAModelIsPassedBack(t *testing.T) {
	f, _ := rolesFixture(t)
	f.respondInTurn(response{http.StatusTooManyRequests, rateLimited})
	token := f.open(t, nil)

	status, _, _ := f.call(t, "/v1/messages", signInHeader(token), messages(model, false))

	if got := f.models(t); status != http.StatusTooManyRequests || len(got) != 1 {
		t.Errorf("status = %d after calls to %q, want the limit after one call: the call names no role", status, got)
	}
}

func TestAServerErrorIsRetriedOnceAfterAWait(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable, 529} {
		t.Run(strconv.Itoa(status), func(t *testing.T) { checkRetried(t, status) })
	}
}

// checkRetried checks that a call answered with the server error status
// is sent once more to the same model after the wait, and that the log
// says so.
func checkRetried(t *testing.T, status int) {
	t.Helper()
	f, w := rolesFixture(t)
	f.respondInTurn(response{status, unavailable}, ok)
	token := f.open(t, nil)

	got, _, body := f.call(t, "/v1/messages", signInHeader(token), messages(role, false))

	if got != http.StatusOK || body != nonStreamed {
		t.Errorf("response = %d %q, want the retry's answer", got, body)
	}
	if got := f.models(t); !equalStrings(got, []string{model, model}) {
		t.Errorf("the provider received calls to %q, want two to %q", got, model)
	}
	if got := w.durations(); len(got) != 1 || got[0] != retryWait {
		t.Errorf("the gateway waited %v, want once %v", got, retryWait)
	}
	lines := f.logLines("retr", role, model, strconv.Itoa(status))
	if len(lines) != 1 || !timeInLog.MatchString(lines[0]) {
		t.Errorf("the invocation's log = %q, want one line with the role, the model, the status and the time", f.invocationLog.String())
	}
	if got := f.totals(t, token); got.Tokens != nonStreamedTokens {
		t.Errorf("Totals() = %+v, want the retry's %d tokens", got, nonStreamedTokens)
	}
}

func TestARetryThatFailsIsPassedBack(t *testing.T) {
	f, w := rolesFixture(t)
	f.respondInTurn(response{529, overloaded})
	token := f.open(t, nil)

	status, _, body := f.call(t, "/v1/messages", signInHeader(token), messages(role, false))

	if status != 529 || body != overloaded {
		t.Errorf("response = %d %q, want the retry's error", status, body)
	}
	if got := f.models(t); len(got) != 2 || len(w.durations()) != 1 {
		t.Errorf("the provider received %d calls after %d waits, want 2 after one", len(got), len(w.durations()))
	}
	if lines := f.logLines("529", "passed back"); len(lines) != 1 {
		t.Errorf("the invocation's log = %q, want a line that the retry's error is passed back", f.invocationLog.String())
	}
}

func TestARetryAndAFallbackEachHappenAtMostOnce(t *testing.T) {
	f, w := rolesFixture(t)
	f.respondInTurn(response{http.StatusServiceUnavailable, unavailable}, response{http.StatusTooManyRequests, rateLimited},
		response{http.StatusServiceUnavailable, unavailable})
	token := f.open(t, nil)

	status, _, _ := f.call(t, "/v1/messages", signInHeader(token), messages(role, false))

	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want the fallback's 503 passed back", status)
	}
	if got := f.models(t); !equalStrings(got, []string{model, model, fallback}) || len(w.durations()) != 1 {
		t.Errorf("the provider received calls to %q after %d waits, want a retry, a fallback and nothing more", got, len(w.durations()))
	}
}

func TestAnErrorAfterTheResponseStartedIsNotRetried(t *testing.T) {
	f, w := rolesFixture(t)
	const started = "event: message_start\n" +
		`data: {"type":"message_start","message":{"model":"claude-opus-5-5","id":"msg_3","type":"message","role":"assistant","content":[],"usage":{"input_tokens":5,"output_tokens":1}}}` + "\n\n" +
		"event: error\n" +
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}` + "\n\n"
	f.provider.respond = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, started)
	}
	token := f.open(t, nil)

	status, _, body := f.call(t, "/v1/messages", signInHeader(token), messages(role, true))

	if status != http.StatusOK || body != started {
		t.Errorf("response = %d %q, want the stream as the provider sent it", status, body)
	}
	if got := f.models(t); len(got) != 1 || len(w.durations()) != 0 {
		t.Errorf("the provider received %d calls after %d waits, want one and no wait", len(got), len(w.durations()))
	}
}

func TestOtherErrorsAreNotRetried(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f, w := rolesFixture(t)
			f.respondInTurn(response{status, `{"type":"error","error":{"type":"error","message":"no"}}`})
			token := f.open(t, nil)

			got, _, _ := f.call(t, "/v1/messages", signInHeader(token), messages(role, false))

			if n := len(f.models(t)); got != status || n != 1 || len(w.durations()) != 0 {
				t.Errorf("status = %d after %d calls, want %d after one", got, n, status)
			}
		})
	}
}

func TestARetryIsRefusedWhenTheCapIsReachedDuringTheWait(t *testing.T) {
	f, w := rolesFixture(t)
	token := f.open(t, &harnessv1.Usage{Tokens: nonStreamedTokens})
	// The first call fails with a server error; during the wait before its
	// retry, a second call of the invocation reaches the token cap.
	f.respondInTurn(response{http.StatusServiceUnavailable, unavailable}, ok)
	w.during = func() {
		if status, _, _ := f.call(t, "/v1/messages", signInHeader(token), messages(role, false)); status != http.StatusOK {
			t.Errorf("the second call = %d, want 200", status)
		}
	}

	status, header, body := f.call(t, "/v1/messages", signInHeader(token), messages(role, false))

	assertCapRefusal(t, status, header, body)
	if got := f.models(t); len(got) != 2 {
		t.Errorf("the provider received %d calls, want 2: the first and the second, and no retry", len(got))
	}
	if got := f.totals(t, token); !got.CapReached || got.Tokens != nonStreamedTokens {
		t.Errorf("Totals() = %+v, want the cap reached at the second call's tokens", got)
	}
	if lines := f.logLines("cap", "not sent"); len(lines) != 1 {
		t.Errorf("the invocation's log = %q, want a line that the retry was not sent at the cap", f.invocationLog.String())
	}
}

func TestAFallbackIsRefusedWhenTheCapIsReachedDuringTheCall(t *testing.T) {
	f, _ := rolesFixture(t)
	token := f.open(t, &harnessv1.Usage{Tokens: nonStreamedTokens})
	// While the provider has the first call, a second call of the
	// invocation reaches the token cap; the provider then answers the first
	// with a limit, and its fallback would be a call over the cap.
	var once sync.Once
	f.provider.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		first := false
		once.Do(func() { first = true })
		if !first {
			answer(w, r, body)
			return
		}
		if status, _, _ := f.call(t, "/v1/messages", signInHeader(token), messages(fallback, false)); status != http.StatusOK {
			t.Errorf("the second call = %d, want 200", status)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, rateLimited)
	}

	status, header, body := f.call(t, "/v1/messages", signInHeader(token), messages(role, false))

	assertCapRefusal(t, status, header, body)
	if got := f.models(t); !equalStrings(got, []string{model, fallback}) {
		t.Errorf("the provider received calls to %q, want the first and the second, and no fallback", got)
	}
	if got := f.totals(t, token); !got.CapReached {
		t.Errorf("Totals() = %+v, want the cap reached", got)
	}
	if lines := f.logLines("cap", "not sent"); len(lines) != 1 {
		t.Errorf("the invocation's log = %q, want a line that the fallback was not sent at the cap", f.invocationLog.String())
	}
}

func TestAHarnessThatEndsTheCallDuringTheWaitGetsNoRetry(t *testing.T) {
	f := newFixtureWith(t, gateway.Config{
		Roles:     map[string]gateway.Role{role: {Model: model, Fallback: fallback}},
		RetryWait: time.Hour,
	})
	f.respondInTurn(response{http.StatusServiceUnavailable, unavailable})
	token := f.open(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url+"/v1/messages", strings.NewReader(messages(role, false)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = signInHeader(token)
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	waitFor(t, func() bool { return len(f.logLines("retr")) == 1 })

	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("the call ended with %v, want context.Canceled", err)
	}
	waitFor(t, func() bool { return len(f.logLines("ended the call", "no retry")) == 1 })
	if n := len(f.provider.received()); n != 1 {
		t.Errorf("the provider received %d calls, want 1", n)
	}
}

// waitFor waits until cond holds, for at most five seconds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("the condition did not hold within five seconds")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestALimitOnARoleWithoutAFallbackIsPassedBack: a model role whose
// fallback is on another adapter has no fallback model in the gateway, so
// the gateway passes the limit back after one call (decisions 0233 and
// 0246).
func TestALimitOnARoleWithoutAFallbackIsPassedBack(t *testing.T) {
	f := newFixtureWith(t, gateway.Config{Roles: map[string]gateway.Role{role: {Model: model}}})
	f.respondInTurn(response{http.StatusTooManyRequests, rateLimited})
	token := f.open(t, nil)

	status, _, body := f.call(t, "/v1/messages", signInHeader(token), messages(role, false))

	if status != http.StatusTooManyRequests || body != rateLimited {
		t.Errorf("response = %d %q, want the limit", status, body)
	}
	if got := f.models(t); !equalStrings(got, []string{model}) {
		t.Errorf("the provider received calls to %q, want one to %s", got, model)
	}
}

// TestACallOfTheFallbacksAdapterGoesToTheFallbacksModel: a model role's
// fallback on another adapter maps the role, for the invocations of that
// adapter, to the fallback's model, with no fallback of its own; the
// invocations of the role's own adapter still go to the role's model
// (decision 0246).
func TestACallOfTheFallbacksAdapterGoesToTheFallbacksModel(t *testing.T) {
	const other = "other-adapter"
	f := newFixtureWith(t, gateway.Config{
		Roles:        map[string]gateway.Role{role: {Model: model}},
		AdapterRoles: map[string]map[string]gateway.Role{other: {role: {Model: fallback}}},
		RetryWait:    retryWait,
	})
	f.respondInTurn(response{http.StatusTooManyRequests, rateLimited})
	token, err := f.gw.Open("inv-2", other, nil, f.invocationLog)
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ := f.call(t, "/v1/messages", signInHeader(token), messages(role, false))
	if status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want the provider's 429 with no fallback", status)
	}
	own := f.open(t, nil)
	f.respondInTurn(ok)
	if status, _, _ := f.call(t, "/v1/messages", signInHeader(own), messages(role, false)); status != http.StatusOK {
		t.Errorf("status of the call of the role's own adapter = %d, want 200", status)
	}
	if got := f.models(t); !equalStrings(got, []string{fallback, model}) {
		t.Errorf("the provider received calls to %q, want one to %s and one to %s", got, fallback, model)
	}
}

// TestNewRefusesABadAdapterRole: an adapter's model role needs a model in
// the price table and has no fallback model (decision 0246).
func TestNewRefusesABadAdapterRole(t *testing.T) {
	u, err := url.Parse("https://api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]gateway.Role{
		"no model":       {},
		"unlisted model": {Model: "claude-unknown"},
		"a fallback":     {Model: model, Fallback: fallback},
	} {
		_, err := gateway.New(gateway.Config{Upstreams: map[gateway.API]*url.URL{gateway.MessagesAPI: u}, AdapterRoles: map[string]map[string]gateway.Role{"other": {role: r}}})
		if err == nil {
			t.Errorf("New with an adapter role with %s succeeded, want an error", name)
		}
	}
}
