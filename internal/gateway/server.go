// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Server serves a gateway on the loopback interface.
type Server struct {
	srv  *http.Server
	url  string
	done chan struct{}
}

// Serve serves the gateway on 127.0.0.1, at a port the operating system
// picks, until Shutdown (decision 0231).
func Serve(g *Gateway) (*Server, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("gateway: listening on 127.0.0.1: %w", err)
	}
	s := &Server{
		srv:  &http.Server{Handler: g, ReadHeaderTimeout: 30 * time.Second},
		url:  "http://" + l.Addr().String(),
		done: make(chan struct{}),
	}
	go func() {
		defer close(s.done)
		if err := s.srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			g.log.Error("gateway stopped serving", "error", err.Error())
		}
	}()
	return s, nil
}

// URL is the base URL that a harness is given, such as
// http://127.0.0.1:41234.
func (s *Server) URL() string { return s.url }

// Shutdown stops the server. It waits for calls in progress until ctx
// ends, and then closes their connections.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.srv.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, s.srv.Close())
	}
	<-s.done
	return err
}
