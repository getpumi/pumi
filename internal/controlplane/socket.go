// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
)

// socketHeaderTimeout is how long the socket's server waits for the
// headers of a request.
const socketHeaderTimeout = 10 * time.Second

// Socket is a Unix socket on which pumi run serves the control plane to
// the person, while it holds the embedded log's lock (decision 0206).
type Socket struct {
	path     string
	listener net.Listener
}

// ListenSocket listens on a Unix socket at path, whose file only the user
// who runs pumi may read and write. A socket's file left at path by a run
// that ended without removing it is removed first; the caller holds the
// embedded log's lock, so no other run serves it. Any other file at path is
// refused.
func ListenSocket(ctx context.Context, path string) (*Socket, error) {
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.Mode()&os.ModeSocket != 0:
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing the socket left at %s: %w", path, err)
		}
	case err == nil:
		return nil, fmt.Errorf("%s is not a socket, so pumi does not replace it with its socket", path)
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("listening on the socket %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("limiting the socket %s to its user: %w", path, err)
	}
	return &Socket{path: path, listener: listener}, nil
}

// Close stops listening and removes the socket's file.
func (s *Socket) Close() error {
	err := s.listener.Close()
	if rerr := os.Remove(s.path); rerr != nil && !errors.Is(rerr, os.ErrNotExist) && err == nil {
		err = rerr
	}
	return err
}

// Serve serves server, a server of Service with the person's identity, on
// the socket until ctx is done, then closes every connection and the
// socket. It returns nil when ctx ended it.
func (s *Socket) Serve(ctx context.Context, server *connect.Server) error {
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: socketHeaderTimeout}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(s.listener) }()
	select {
	case <-ctx.Done():
		err := srv.Close()
		if serr := <-served; err == nil && !errors.Is(serr, http.ErrServerClosed) {
			err = serr
		}
		if cerr := s.Close(); err == nil && !errors.Is(cerr, net.ErrClosed) {
			err = cerr
		}
		return err
	case err := <-served:
		_ = s.Close()
		return fmt.Errorf("serving the socket %s: %w", s.path, err)
	}
}

// SocketTransport returns the transport of a client of the control plane
// on the Unix socket at path.
func SocketTransport(path string) connect.Transport {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
	// The host of the URL is not used: every connection goes to the socket.
	return connecthttp.NewTransport(client, "http://pumi")
}

// SocketClient returns a client of the control plane on the Unix socket at
// path, which the generated clients wrap. A call when no run serves the
// socket fails with CodeUnavailable.
func SocketClient(path string) *connect.Client {
	return connect.NewClient(SocketTransport(path))
}

// NotServed reports whether err is the error of a call of a client from
// SocketClient when no run serves the socket: a call that fails with
// CodeUnavailable.
func NotServed(err error) bool {
	return connect.CodeOf(err) == connect.CodeUnavailable
}
