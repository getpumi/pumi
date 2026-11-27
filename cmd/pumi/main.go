// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Command pumi is the Pumi command-line interface. It also runs Pumi in
// embedded mode, with every component in one process (decision 0188).
//
// A main package under cmd/ is the only kind of package that may import more
// than one component, and only to wire them together. The commands are in
// internal/cli; this package gives them the person's session with the
// control plane and the run of embedded mode, which wires the kernel, the
// embedded control plane and the in-process runner on the clone's log.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"getpumi.dev/pumi/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := execute(ctx, ".", os.Args[1:], os.Stdout, os.Stderr)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// execute runs the pumi command that args name in the clone that dir is
// in.
func execute(ctx context.Context, dir string, args []string, stdout, stderr io.Writer) error {
	return cli.Main(ctx, args, cli.Env{
		Stdout: stdout, Stderr: stderr,
		Connect: func(ctx context.Context, config string) (*cli.Session, error) {
			return connectPerson(ctx, dir, config)
		},
		Run: func(ctx context.Context, config string, out io.Writer) error {
			return runEmbedded(ctx, dir, config, out)
		},
		Branch: func(ctx context.Context, b cli.Branch) (string, error) {
			return writeBranch(ctx, dir, b)
		},
	})
}
