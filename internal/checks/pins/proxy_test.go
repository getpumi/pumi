// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package pins

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"
)

// response is one recorded answer of the module proxy.
type response struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// newProxy serves the responses in testdata/proxy.json, which were recorded
// from proxy.golang.org on 2026-11-19, with the responses in overrides in
// place of or beside them. A request for any other path fails the test, so
// the tests also show which requests the check makes.
func newProxy(t *testing.T, overrides map[string]response) *Proxy {
	t.Helper()
	data, err := os.ReadFile("testdata/proxy.json")
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Responses map[string]response `json:"responses"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	for k, v := range overrides {
		rec.Responses[k] = v
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, ok := rec.Responses[r.URL.Path]
		if !ok {
			t.Errorf("request for %s, which is not recorded", r.URL.Path)
			http.Error(w, "not recorded", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(resp.Status)
		_, _ = w.Write([]byte(resp.Body))
	}))
	t.Cleanup(srv.Close)
	return &Proxy{BaseURL: srv.URL, Client: srv.Client()}
}

func TestLatest(t *testing.T) {
	p := newProxy(t, nil)
	v, found, err := p.Latest(t.Context(), "github.com/golangci/golangci-lint/v2")
	if err != nil || !found || v != "v2.15.0" {
		t.Errorf("Latest(golangci-lint/v2) = %q, %v, %v; want v2.15.0, true, nil", v, found, err)
	}
	v, found, err = p.Latest(t.Context(), "github.com/golangci/golangci-lint/v3")
	if err != nil || found {
		t.Errorf("Latest(golangci-lint/v3) = %q, %v, %v; want not found", v, found, err)
	}
}

func TestLatestEscapesUpperCase(t *testing.T) {
	p := newProxy(t, map[string]response{
		"/github.com/!burnt!sushi/toml/@latest": {200, `{"Version":"v1.5.0","Time":"2025-03-13T09:13:49Z"}`},
	})
	v, found, err := p.Latest(t.Context(), "github.com/BurntSushi/toml")
	if err != nil || !found || v != "v1.5.0" {
		t.Errorf("Latest(BurntSushi/toml) = %q, %v, %v; want v1.5.0, true, nil", v, found, err)
	}
}

func TestLatestGone(t *testing.T) {
	p := newProxy(t, map[string]response{
		"/example.com/gone/@latest": {410, "gone"},
	})
	if _, found, err := p.Latest(t.Context(), "example.com/gone"); err != nil || found {
		t.Errorf("Latest(gone) = %v, %v; want not found", found, err)
	}
}

func TestLatestServerError(t *testing.T) {
	p := newProxy(t, map[string]response{
		"/example.com/broken/@latest": {502, "bad gateway"},
	})
	if _, _, err := p.Latest(t.Context(), "example.com/broken"); err == nil {
		t.Error("Latest(broken) returned no error for status 502")
	}
}

func TestGoReleases(t *testing.T) {
	p := newProxy(t, nil)
	got, err := p.GoReleases(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSortedFunc(got, GoVersion.Compare) {
		t.Error("GoReleases() is not sorted")
	}
	if last := got[len(got)-1]; last != (GoVersion{1, 27, 4}) {
		t.Errorf("newest = %v, want 1.27.4", last)
	}
	// go1.20 is listed without a patch number; release candidates and the
	// darwin-arm64 lines are left out, and each release appears once.
	for _, v := range []GoVersion{{1, 20, 0}, {1, 26, 11}, {1, 27, 0}} {
		if n := countOf(got, v); n != 1 {
			t.Errorf("%v appears %d times, want 1", v, n)
		}
	}
}

func countOf(vs []GoVersion, v GoVersion) int {
	n := 0
	for _, x := range vs {
		if x == v {
			n++
		}
	}
	return n
}

func TestGoReleaseTime(t *testing.T) {
	p := newProxy(t, nil)
	got, err := p.GoReleaseTime(t.Context(), GoVersion{1, 27, 1})
	want := time.Date(2026, 8, 28, 16, 20, 6, 0, time.UTC)
	if err != nil || !got.Equal(want) {
		t.Errorf("GoReleaseTime(1.27.1) = %v, %v; want %v", got, err, want)
	}
}
