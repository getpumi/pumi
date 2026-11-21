// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectinprocess"

	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
	"getpumi.dev/pumi/api/policy"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/controlplane/conformance"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
)

// TestConformanceInProcess runs the conformance suite against the embedded
// control plane through connectinprocess, the transport of embedded mode
// (decision 0161).
func TestConformanceInProcess(t *testing.T) {
	conformance.Run(t, start(func(_ *testing.T, server *connect.Server) connect.Transport {
		return connectinprocess.New(server)
	}))
}

// TestConformanceOverHTTP runs the conformance suite against the embedded
// control plane over HTTP to a test server, the transport of remote mode
// (decision 0161).
func TestConformanceOverHTTP(t *testing.T) {
	conformance.Run(t, start(func(t *testing.T, server *connect.Server) connect.Transport {
		mux := http.NewServeMux()
		connecthttp.Mount(mux, server)
		ts := httptest.NewServer(mux)
		t.Cleanup(ts.Close)
		return connecthttp.NewTransport(ts.Client(), ts.URL)
	}))
}

// callerOf gives the caller of the control plane for each caller of the
// suite.
var callerOf = map[conformance.Caller]controlplane.Caller{
	conformance.Kernel: controlplane.CallerKernel,
	conformance.Person: controlplane.CallerPerson,
	conformance.Runner: controlplane.CallerRunner,
}

// start returns a conformance.Start that opens a new embedded log, serves
// it with one server for each caller, wired with that caller's identity,
// and reaches each server through the transport that transport returns.
// The control plane's clock is a fake one that the suite advances.
func start(transport func(*testing.T, *connect.Server) connect.Transport) conformance.Start {
	return func(t *testing.T) conformance.Plane {
		t.Helper()
		clock := newFakeClock()
		cfg := config()
		cfg.Clock = clock
		svc, err := controlplane.New(openLog(t), cfg)
		if err != nil {
			t.Fatal(err)
		}
		dial := func(id controlplane.Identity) *connect.Client {
			server, err := svc.Server(id)
			if err != nil {
				t.Fatalf("Server for %v: %v", id, err)
			}
			return connect.NewClient(transport(t, server))
		}
		clients := map[conformance.Caller]*connect.Client{}
		for _, caller := range conformance.Callers {
			clients[caller] = dial(identity(callerOf[caller]))
		}
		client := func(c conformance.Caller) *connect.Client {
			client, ok := clients[c]
			if !ok {
				t.Fatalf("the suite asked for a client of the unknown caller %q", c)
			}
			return client
		}
		r := &runners{dial: dial, clients: map[string]runnerv1connect.RunnerServiceClient{}}
		return conformance.Plane{
			Clients: func(c conformance.Caller) controlplanev1connect.ControlPlaneServiceClient {
				return controlplanev1connect.NewControlPlaneServiceClient(client(c))
			},
			Runner: r.runner,
			RunnerServiceAs: func(c conformance.Caller) runnerv1connect.RunnerServiceClient {
				return runnerv1connect.NewRunnerServiceClient(client(c))
			},
			Advance: clock.Advance,
		}
	}
}

// runners gives the suite one RunnerService client for each runner name.
type runners struct {
	mu      sync.Mutex
	dial    func(controlplane.Identity) *connect.Client
	clients map[string]runnerv1connect.RunnerServiceClient
}

// runner returns the client of the runner name, with the roles given the
// first time the name is used.
func (r *runners) runner(name string, roles ...conformance.Role) runnerv1connect.RunnerServiceClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[name]; ok {
		return c
	}
	id := controlplane.Identity{Caller: controlplane.CallerRunner, Principal: name, Tenant: embeddedlog.Tenant}
	for _, role := range roles {
		id.Roles = append(id.Roles, string(role))
	}
	r.clients[name] = runnerv1connect.NewRunnerServiceClient(r.dial(id))
	return r.clients[name]
}

// permitAll is a lower policy set that permits every request, with which
// only the invariants and request validation deny.
const permitAll = "@id(\"test.permit-all\")\npermit (principal, action, resource);\n"

// config is the wiring of the control planes under test.
func config() controlplane.Config {
	return controlplane.Config{
		Policies: []controlplane.PolicyFile{{Name: "permit-all.cedar", Text: permitAll}},
		Settings: controlplane.Settings{
			MaxAttempts: 3, Reviewers: 1, WarningsCap: 5,
			UsageBudget: policy.Caps{Tokens: 1_000_000, CostMicrodollars: 10_000_000, Seconds: 3600},
		},
		HardCaps: policy.DefaultHardCaps(),
		Project:  controlplane.Project{Name: "pumi", ProtectedPaths: []string{".github/**"}},
	}
}

// newService returns the embedded control plane, wired with config, on a
// new log.
func newService(t *testing.T) *controlplane.Service {
	t.Helper()
	svc, err := controlplane.New(openLog(t), config())
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// identity returns the identity of a client of the kind caller.
func identity(caller controlplane.Caller) controlplane.Identity {
	roles := []string{"maintainer"}
	if caller == controlplane.CallerRunner {
		roles = []string{"gate"}
	}
	return controlplane.Identity{Caller: caller, Principal: string(caller) + "-1", Roles: roles, Tenant: embeddedlog.Tenant}
}

// openLog opens the embedded log of a new git clone and closes it when the
// test ends.
func openLog(t *testing.T) *embeddedlog.Log {
	t.Helper()
	log, err := embeddedlog.Open(t.Context(), newClone(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// newClone creates a git repository with one commit in a temporary
// directory and returns its working tree, with the user's and the system's
// git configuration turned off. The commands have constant arguments,
// because gosec reports a command built from variables (G204).
func newClone(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "main")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	git(t, dir, exec.CommandContext(t.Context(), "git", "init", "--quiet", "--initial-branch=main"))
	git(t, dir, exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--allow-empty", "--message=first"))
	return dir
}

// git runs cmd in dir.
func git(t *testing.T, dir string, cmd *exec.Cmd) {
	t.Helper()
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Author", "GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Committer", "GIT_COMMITTER_EMAIL=committer@example.com")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, stderr.String())
	}
}
