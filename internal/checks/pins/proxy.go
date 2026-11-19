// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package pins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
)

// DefaultProxy is the Go module proxy that make pins asks.
const DefaultProxy = "https://proxy.golang.org"

// toolchainModule is the module whose versions are the Go releases, one for
// each port, such as v0.0.1-go1.27.4.linux-amd64.
const toolchainModule = "golang.org/toolchain"

// toolchainPort is the port whose versions the report reads. Every Go
// release is published for it.
const toolchainPort = "linux-amd64"

// maxResponse limits how much of a response the report reads. The list of
// Go releases is about 200 KB.
const maxResponse = 16 << 20

// Proxy asks a Go module proxy, which speaks the GOPROXY protocol, about
// releases.
type Proxy struct {
	// BaseURL is the proxy's URL, such as DefaultProxy.
	BaseURL string
	// Client sends the requests.
	Client *http.Client
}

// errNotFound is returned by get when the proxy has no such module or
// version.
var errNotFound = errors.New("not found")

// info is the proxy's answer about one version.
type info struct {
	Version string
	Time    time.Time
}

// Latest returns the version that the go command takes for module@latest.
// found is false when the proxy knows no version of the module.
func (p *Proxy) Latest(ctx context.Context, module string) (version string, found bool, err error) {
	var v info
	err = p.getJSON(ctx, escapePath(module)+"/@latest", &v)
	if errors.Is(err, errNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v.Version, true, nil
}

// GoReleases returns the Go releases, release candidates and betas left out,
// from the oldest to the newest.
func (p *Proxy) GoReleases(ctx context.Context) ([]GoVersion, error) {
	data, err := p.get(ctx, toolchainModule+"/@v/list")
	if err != nil {
		return nil, err
	}
	var out []GoVersion
	for _, line := range strings.Fields(string(data)) {
		s, ok := strings.CutPrefix(line, "v0.0.1-go")
		if !ok {
			continue
		}
		s, ok = strings.CutSuffix(s, "."+toolchainPort)
		if !ok {
			continue
		}
		if v, err := ParseGoVersion(s); err == nil {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the proxy lists no Go release of %s", toolchainModule)
	}
	slices.SortFunc(out, GoVersion.Compare)
	return slices.Compact(out), nil
}

// GoReleaseTime returns when the proxy published the Go release v.
func (p *Proxy) GoReleaseTime(ctx context.Context, v GoVersion) (time.Time, error) {
	var i info
	if err := p.getJSON(ctx, toolchainModule+"/@v/"+toolchainVersion(v)+".info", &i); err != nil {
		return time.Time{}, err
	}
	if i.Time.IsZero() {
		return time.Time{}, fmt.Errorf("the proxy gives no time for Go %v", v)
	}
	return i.Time, nil
}

// toolchainVersion returns the golang.org/toolchain version of v. A release
// before Go 1.21 has no patch number in its name when its patch number is 0.
func toolchainVersion(v GoVersion) string {
	name := v.String()
	if v.Patch == 0 && v.Compare(GoVersion{Major: 1, Minor: 21}) < 0 {
		name = v.minorString()
	}
	return "v0.0.1-go" + name + "." + toolchainPort
}

func (p *Proxy) getJSON(ctx context.Context, path string, v any) error {
	data, err := p.get(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// get returns the body of the proxy's answer for path. The proxy answers
// 404 or 410 for a module or version that it does not have.
func (p *Proxy) get(ctx context.Context, path string) ([]byte, error) {
	url := strings.TrimSuffix(p.BaseURL, "/") + "/" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return data, nil
	case http.StatusNotFound, http.StatusGone:
		return nil, fmt.Errorf("%s: %w", url, errNotFound)
	}
	return nil, fmt.Errorf("%s: %s: %s", url, resp.Status, strings.TrimSpace(string(data)))
}

// escapePath escapes a module path for the proxy protocol, which writes each
// upper-case letter as an exclamation mark and the lower-case letter.
func escapePath(path string) string {
	var b strings.Builder
	for _, r := range path {
		if unicode.IsUpper(r) {
			b.WriteByte('!')
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}
