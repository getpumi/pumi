// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/controlplane/conformance"
)

// serveSocket serves server on a new Unix socket until the test ends, and
// returns the socket's path.
func serveSocket(t *testing.T, server *connect.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "socket")
	sock, err := controlplane.ListenSocket(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sock.Serve(ctx, server) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return path
}

// sockets counts the sockets of the conformance run, so that each has its
// own short path.
var sockets atomic.Int64

// TestConformanceOverASocket runs the conformance suite against the
// embedded control plane over the Unix socket that pumi run serves
// (decision 0206).
func TestConformanceOverASocket(t *testing.T) {
	dir := t.TempDir()
	conformance.Run(t, start(func(t *testing.T, server *connect.Server) connect.Transport {
		path := filepath.Join(dir, strconv.FormatInt(sockets.Add(1), 10))
		sock, err := controlplane.ListenSocket(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- sock.Serve(ctx, server) }()
		t.Cleanup(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Serve: %v", err)
			}
		})
		return controlplane.SocketTransport(path)
	}))
}

// personServer returns the server of the person on a new embedded control
// plane.
func personServer(t *testing.T) *connect.Server {
	t.Helper()
	svc, err := controlplane.New(openLog(t), config())
	if err != nil {
		t.Fatal(err)
	}
	server, err := svc.Server(identity(controlplane.CallerPerson))
	if err != nil {
		t.Fatal(err)
	}
	return server
}

// TestTheSocketIsOnlyForItsUser: the socket's file can be read and written
// only by the user who runs pumi (decision 0206).
func TestTheSocketIsOnlyForItsUser(t *testing.T) {
	path := serveSocket(t, personServer(t))
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Errorf("the socket's mode is %v, want a socket with the permissions 0600", info.Mode())
	}
}

// TestASocketLeftByAnEndedRunIsReplaced: a run that was killed leaves its
// socket's file, and the next run listens at the same path.
func TestASocketLeftByAnEndedRunIsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socket")
	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	left, ok := listener.(*net.UnixListener)
	if !ok {
		t.Fatalf("the listener is a %T, not a Unix socket's", listener)
	}
	// A killed process does not remove its socket's file.
	left.SetUnlinkOnClose(false)
	if err := left.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the closed socket left no file: %v", err)
	}
	sock, err := controlplane.ListenSocket(t.Context(), path)
	if err != nil {
		t.Fatalf("ListenSocket at the path of a socket left behind: %v", err)
	}
	server := personServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sock.Serve(ctx, server) }()
	client := controlplanev1connect.NewControlPlaneServiceClient(controlplane.SocketClient(path))
	if _, err := client.Query(t.Context(), &controlplanev1.QueryRequest{}); err != nil {
		t.Errorf("Query over the new socket: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Serve: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Serve left the socket's file after it ended: %v", err)
	}
}

// TestACallWithNoServerIsUnavailable: with no run serving the socket, a
// call fails with CodeUnavailable, which the pumi commands read as no run.
func TestACallWithNoServerIsUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "socket")
	client := controlplanev1connect.NewControlPlaneServiceClient(controlplane.SocketClient(path))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := client.Query(ctx, &controlplanev1.QueryRequest{})
	if connect.CodeOf(err) != connect.CodeUnavailable || !controlplane.NotServed(err) {
		t.Errorf("Query with no server = %v, want CodeUnavailable, which NotServed reports", err)
	}
	if controlplane.NotServed(nil) || controlplane.NotServed(connect.NewError(connect.CodeInternal, "internal")) {
		t.Error("NotServed reports a call that succeeded or failed with another code")
	}
}
