// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package pins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultCrates is the crates.io API that make pins asks.
const DefaultCrates = "https://crates.io"

// userAgent names the pin report to crates.io, which refuses requests
// without a User-Agent and asks for one that says who sends them.
const userAgent = "getpumi-pins (https://github.com/getpumi/pumi)"

// Crates asks the crates.io API about releases of Rust crates.
type Crates struct {
	// BaseURL is the API's URL, such as DefaultCrates.
	BaseURL string
	// Client sends the requests.
	Client *http.Client
}

// MaxStableVersion returns the newest version of the crate that is not a
// prerelease, as crates.io reports it.
func (c *Crates) MaxStableVersion(ctx context.Context, name string) (string, error) {
	u := strings.TrimSuffix(c.BaseURL, "/") + "/api/v1/crates/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return "", fmt.Errorf("%s: %w", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s: %s", u, resp.Status, strings.TrimSpace(string(data)))
	}
	var body struct {
		Crate struct {
			MaxStableVersion string `json:"max_stable_version"`
		} `json:"crate"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return "", fmt.Errorf("%s: %w", u, err)
	}
	v := body.Crate.MaxStableVersion
	if _, ok := parseSemver("v" + v); !ok || isPrerelease("v"+v) {
		return "", fmt.Errorf("%s: the newest stable version %q is not a version such as 1.2.3", u, v)
	}
	return v, nil
}

// Sources are the services that the pin report asks.
type Sources struct {
	// Proxy is the Go module proxy.
	Proxy *Proxy
	// Crates is the crates.io API.
	Crates *Crates
}
