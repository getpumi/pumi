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
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"connectrpc.com/connect/v2/connectinprocess"

	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
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
func start(transport func(*testing.T, *connect.Server) connect.Transport) conformance.Start {
	return func(t *testing.T) conformance.Clients {
		t.Helper()
		log := openLog(t)
		svc := controlplane.New(log)
		clients := map[conformance.Caller]controlplanev1connect.ControlPlaneServiceClient{}
		for _, caller := range conformance.Callers {
			server, err := svc.Server(controlplane.Identity{Caller: callerOf[caller], Tenant: embeddedlog.Tenant})
			if err != nil {
				t.Fatalf("Server for a %s: %v", caller, err)
			}
			clients[caller] = controlplanev1connect.NewControlPlaneServiceClient(connect.NewClient(transport(t, server)))
		}
		return func(c conformance.Caller) controlplanev1connect.ControlPlaneServiceClient {
			client, ok := clients[c]
			if !ok {
				t.Fatalf("the suite asked for a client of the unknown caller %q", c)
			}
			return client
		}
	}
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
