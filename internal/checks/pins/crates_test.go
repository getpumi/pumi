// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package pins

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// newCrates serves the responses in testdata/crates.json, which were
// recorded from crates.io on 2026-11-20, with the responses in overrides in
// place of or beside them. A request for any other path, or one without a
// User-Agent that names Pumi, fails the test: crates.io refuses requests
// without a User-Agent.
func newCrates(t *testing.T, overrides map[string]response) *Crates {
	t.Helper()
	data, err := os.ReadFile("testdata/crates.json")
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
		if strings.HasPrefix(k, "/api/v1/crates/") {
			rec.Responses[k] = v
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.UserAgent(), "getpumi") {
			t.Errorf("request for %s with User-Agent %q, want one that names getpumi", r.URL.Path, r.UserAgent())
		}
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
	return &Crates{BaseURL: srv.URL, Client: srv.Client()}
}

// sources returns a module proxy and a crates.io API that serve the
// recorded responses, with overrides.
func sources(t *testing.T, overrides map[string]response) Sources {
	t.Helper()
	return Sources{Proxy: newProxy(t, overrides), Crates: newCrates(t, overrides)}
}

func TestMaxStableVersion(t *testing.T) {
	v, err := newCrates(t, nil).MaxStableVersion(t.Context(), "cedar-policy-cli")
	if err != nil || v != "4.15.0" {
		t.Errorf("MaxStableVersion(cedar-policy-cli) = %q, %v; want 4.15.0, nil", v, err)
	}
}

func TestMaxStableVersionErrors(t *testing.T) {
	for name, resp := range map[string]response{
		"unknown crate": {404, `{"errors":[{"detail":"crate does not exist"}]}`},
		"server error":  {500, "boom"},
		"not JSON":      {200, "<html>"},
		"no version":    {200, `{"crate":{"name":"x"}}`},
		"not a version": {200, `{"crate":{"name":"x","max_stable_version":"latest"}}`},
		"a prerelease":  {200, `{"crate":{"name":"x","max_stable_version":"5.0.0-rc.1"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCrates(t, map[string]response{"/api/v1/crates/x": resp})
			if v, err := c.MaxStableVersion(t.Context(), "x"); err == nil {
				t.Errorf("MaxStableVersion(x) = %q, want an error", v)
			}
		})
	}
}
