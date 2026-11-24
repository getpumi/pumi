// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// gatewayPrefix starts each line that the gateway writes to an
// invocation's log.
const gatewayPrefix = "pumi gateway: "

// maxDiscard is how much of the body of a failed answer the gateway reads
// before it closes it and sends the call again.
const maxDiscard = 1 << 20

// checkRoles refuses a model role without a model or a fallback model, with
// a model that the price table does not list, or with a fallback that is
// its own model (decision 0233). The price table lists only models of
// Anthropic's Messages API, so a role's two models use the same API.
func checkRoles(roles map[string]Role) error {
	var errs []error
	for name, r := range roles {
		switch {
		case name == "":
			errs = append(errs, errors.New("gateway: a model role has no name"))
		case r.Model == "" || r.Fallback == "":
			errs = append(errs, fmt.Errorf("gateway: the model role %q needs a model and a fallback model", name))
		case r.Model == r.Fallback:
			errs = append(errs, fmt.Errorf("gateway: the model role %q has %s as its model and as its fallback model", name, r.Model))
		default:
			for _, m := range []string{r.Model, r.Fallback} {
				if _, ok := Price(m); !ok {
					errs = append(errs, fmt.Errorf("gateway: the model role %q names the model %q, which the price table (version %s) does not list", name, m, PricesVersion))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// withModel returns the uncompressed body of a call of the Messages API
// with its top-level model replaced by model, and every other byte as it
// was.
func withModel(plain []byte, model string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(plain))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("the body is not a JSON object")
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keyEnd := dec.InputOffset()
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		if key != "model" {
			continue
		}
		end := int(dec.InputOffset())
		start := end - len(value)
		if start < int(keyEnd) || !bytes.Equal(plain[start:end], value) {
			return nil, errors.New("the model's value could not be found in the body")
		}
		name, err := json.Marshal(model)
		if err != nil {
			return nil, err
		}
		return slices.Concat(plain[:start], name, plain[end:]), nil
	}
	return nil, errors.New("the body names no model")
}

// sleep waits for d, or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// attempts sends a counted call to the provider, and sends it once more
// when the first answer allows it, before any of the answer reaches the
// harness (decision 0233): to the role's fallback model after a usage or
// rate limit, and to the same model after a wait after a server error.
// Each happens at most once for a call. The cap is checked before each
// further call. Any other call it passes to base unchanged.
type attempts struct {
	g    *Gateway
	base http.RoundTripper
}

func (a *attempts) RoundTrip(req *http.Request) (*http.Response, error) {
	c, ok := req.Context().Value(callKey{}).(*call)
	if !ok {
		return a.base.RoundTrip(req)
	}
	tried := &tries{}
	for {
		resp, err := a.base.RoundTrip(req)
		if err != nil {
			return nil, err
		}
		again, err := a.again(req.Context(), c, tried, resp)
		if err != nil {
			return nil, err
		}
		if !again {
			return resp, nil
		}
		if a.g.refuseAtCap(c.session) {
			a.g.note(c, "the invocation reached its token or cost cap; the further call on %s was not sent", c.model)
			return capResponse(req), nil
		}
		req = resend(req, c.body)
	}
}

// tries is what a call has had: a fallback, a retry or both.
type tries struct {
	fellBack, retried bool
}

// again reports whether the call is sent once more after the answer resp,
// which it then discards: to the fallback model after a limit, or after
// the wait after a server error. An error ends the call.
func (a *attempts) again(ctx context.Context, c *call, tried *tries, resp *http.Response) (bool, error) {
	g, status := a.g, resp.StatusCode
	switch {
	case status == http.StatusTooManyRequests && c.fallback != "" && !tried.fellBack:
		tried.fellBack = true
		discard(resp)
		g.note(c, "the provider answered %s with %s; the call fell back to the role's fallback model %s", c.describe(), answerKind(status), c.fallback)
		body, err := withModel(c.body, c.fallback)
		if err != nil {
			return false, fmt.Errorf("gateway: setting the fallback model: %w", err)
		}
		c.model, c.body = c.fallback, body
		return true, nil
	case status >= 500 && status <= 599 && !tried.retried:
		tried.retried = true
		discard(resp)
		g.note(c, "the provider answered %s with %s before any of the answer was sent to the harness; the gateway retries it on the same model after %s", c.describe(), answerKind(status), g.cfg.RetryWait)
		if err := g.wait(ctx, g.cfg.RetryWait); err != nil {
			g.note(c, "the harness ended the call on %s during the wait; no retry was sent", c.model)
			return false, err
		}
		return true, nil
	}
	if (tried.fellBack || tried.retried) && (status < 200 || status > 299) {
		g.note(c, "the provider answered the further call on %s with %s; the answer is passed back to the harness", c.model, answerKind(status))
	}
	return false, nil
}

// answerKind names the kind of a failed answer in a line of the log.
func answerKind(status int) string {
	switch {
	case status == http.StatusTooManyRequests:
		return "429, a usage or rate limit"
	case status >= 500 && status <= 599:
		return "the server error " + strconv.Itoa(status)
	default:
		return strconv.Itoa(status)
	}
}

// describe names the call in a line of the log: its model role, when it
// named one, and its model.
func (c *call) describe() string {
	if c.role == "" {
		return "the call on " + c.model
	}
	return fmt.Sprintf("the call of the model role %s on %s", c.role, c.model)
}

// note writes a line about a call's fallback or retry to the invocation's
// log, with the time, and to the gateway's log.
func (g *Gateway) note(c *call, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	g.log.Info("gateway: "+msg, "invocation", c.session.invocation)
	if c.session.log == nil {
		return
	}
	line := gatewayPrefix + time.Now().UTC().Format(time.RFC3339) + ": " + msg + " (decision 0233)\n"
	_, _ = io.WriteString(c.session.log, line)
}

// discard reads a little of an answer that the harness will not get, so
// that its connection can be used again, and closes it.
func discard(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDiscard))
	_ = resp.Body.Close()
}

// resend returns req with body, for one more call. The body is encoded
// as the first call's was.
func resend(req *http.Request, body []byte) *http.Request {
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	out.ContentLength = int64(len(body))
	return out
}

// capResponse is the gateway's refusal of a further call at the cap, in
// the form that refuse writes.
func capResponse(req *http.Request) *http.Response {
	b, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": "permission_error", "message": capMessage},
	})
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-Should-Retry", "false")
	return &http.Response{
		Status:        strconv.Itoa(http.StatusForbidden) + " " + http.StatusText(http.StatusForbidden),
		StatusCode:    http.StatusForbidden,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(strings.NewReader(string(b))),
		ContentLength: int64(len(b)),
		Request:       req,
	}
}
