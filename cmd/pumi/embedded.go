// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	"getpumi.dev/pumi/api/gen/pumi/controlplane/v1/controlplanev1connect"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
	"getpumi.dev/pumi/internal/cli"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/controlplane/blobstore"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
	"getpumi.dev/pumi/internal/gateway"
	"getpumi.dev/pumi/internal/kernel"
	"getpumi.dev/pumi/internal/runner"
)

// socketName is the name of the socket of pumi run in the log's directory
// (decision 0206).
const socketName = "socket"

// The principals of the kernel and the in-process runner, and the role of
// the person until the profiles of epic E10 give roles (decision 0208).
const (
	kernelPrincipal = "kernel"
	runnerPrincipal = "runner"
	personRole      = "maintainer"
)

// person returns the person's identity: the git user.email of the clone
// that dir is in (decision 0208). The arguments of git are constant,
// because gosec reports a command built from variables (G204).
func person(ctx context.Context, dir string) (controlplane.Identity, error) {
	cmd := exec.CommandContext(ctx, "git", "config", "--get", "user.email")
	cmd.Dir = dir
	out, err := cmd.Output()
	email := strings.TrimSpace(string(out))
	var exit *exec.ExitError
	switch {
	case err != nil && !errors.As(err, &exit):
		return controlplane.Identity{}, fmt.Errorf("cannot read the clone's git user.email: %w", err)
	case email == "":
		return controlplane.Identity{}, errors.New("the clone has no git user.email, so pumi cannot act as a person (decision 0208)")
	}
	return controlplane.Identity{
		Caller: controlplane.CallerPerson, Principal: email, Roles: []string{personRole}, Tenant: embeddedlog.Tenant,
	}, nil
}

// connectPerson returns the person's session with the control plane of the clone
// that dir is in: through the socket of the pumi run that holds the log,
// or, when no run serves it, on the log and the local blob store opened
// here with the configuration file, which is then required (decision
// 0206).
func connectPerson(ctx context.Context, dir, configPath string) (*cli.Session, error) {
	id, err := person(ctx, dir)
	if err != nil {
		return nil, err
	}
	var cfg *config
	if configPath != "" {
		if cfg, err = readConfig(configPath); err != nil {
			return nil, err
		}
	}
	logDir, err := embeddedlog.DirOf(ctx, dir)
	if err != nil {
		return nil, err
	}
	conn := controlplane.SocketClient(filepath.Join(logDir, socketName))
	client := controlplanev1connect.NewControlPlaneServiceClient(conn)
	_, err = client.Query(ctx, &controlplanev1.QueryRequest{Limit: 1})
	switch {
	case err == nil:
		return &cli.Session{
			Client: client, View: controlplanev1connect.NewViewServiceClient(conn), Person: id.Principal,
			Close: func() error { return nil },
		}, nil
	case !controlplane.NotServed(err):
		return nil, fmt.Errorf("the socket of pumi run: %w", err)
	case cfg == nil:
		return nil, errors.New("no pumi run serves the clone's log, and opening it needs the configuration file: give --config")
	}
	return openPerson(ctx, dir, cfg, id)
}

// openPerson returns the person's session with the embedded control plane
// on the log of the clone that dir is in and the local blob store beside
// it, opened in this process.
func openPerson(ctx context.Context, dir string, cfg *config, id controlplane.Identity) (*cli.Session, error) {
	log, err := embeddedlog.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	blobs, err := blobstore.OpenLocal(log.Dir())
	if err != nil {
		_ = log.Close()
		return nil, err
	}
	closeAll := func() error { return errors.Join(blobs.Close(), log.Close()) }
	svc, err := newPlane(log, blobs, cfg)
	if err != nil {
		_ = closeAll()
		return nil, err
	}
	conn, err := svc.Client(id)
	if err != nil {
		_ = closeAll()
		return nil, err
	}
	return &cli.Session{
		Client: controlplanev1connect.NewControlPlaneServiceClient(conn), View: controlplanev1connect.NewViewServiceClient(conn),
		Person: id.Principal, Close: closeAll,
	}, nil
}

// newPlane returns the embedded control plane on log with the
// configuration file cfg, which reads stored logs from blobs and gives the
// file's workflow.
func newPlane(log *embeddedlog.Log, blobs blobstore.Store, cfg *config) (*controlplane.Service, error) {
	wf, err := cfg.workflow()
	if err != nil {
		return nil, err
	}
	plane := cfg.plane()
	plane.Blobs = blobs
	plane.Workflow = wf.Definition()
	return controlplane.New(log, plane)
}

// runEmbedded runs embedded mode on the clone that dir is in, with the
// configuration file at configPath, until ctx is done, and returns nil then
// (decisions 0188 and 0198): the embedded control plane on the clone's log,
// with the local blob store and the file's workflow, which ViewService
// reads, served to the person on the socket beside it (decision 0206), the
// kernel on the file's workflow, starting each plan from the trunk's tree
// (decision 0209), the model gateway on 127.0.0.1 (decision 0231), and the
// in-process runner with the file's adapters, the adapter of each agent
// run picked by its model role (decision 0245) or by the role's fallback on
// another adapter after a limit (decision 0246), whose agent runs go through
// the gateway when their adapter is in the API key mode, and otherwise talk
// to the provider directly while the runner counts their usage (decisions
// 0240 and 0241), whose output goes to out and whose output files go to the
// local blob store beside the log (decision 0232). It refuses a workflow
// that names a model role which the file neither lists nor gives a default
// role for, and gives each such role the default (decisions 0233 and 0257),
// and records the maker of each agent run's model (decision 0261). It
// stops when any of them fails.
func runEmbedded(ctx context.Context, dir, configPath string, out io.Writer) error {
	cfg, err := readConfig(configPath)
	if err != nil {
		return err
	}
	wf, err := cfg.workflow()
	if err != nil {
		return err
	}
	if err := resolveRoles(cfg, wf); err != nil {
		return err
	}
	id, err := person(ctx, dir)
	if err != nil {
		return err
	}
	log, err := embeddedlog.Open(ctx, dir)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	// The local blob store is beside the log, in the pumi directory
	// (decision 0232). It is opened while the log's lock is held, which
	// lets it remove the temporary files that a crash left.
	blobs, err := blobstore.OpenLocal(log.Dir())
	if err != nil {
		return err
	}
	defer func() { _ = blobs.Close() }()
	svc, err := newPlane(log, blobs, cfg)
	if err != nil {
		return err
	}
	kernelClient, runnerClient, err := newClients(svc)
	if err != nil {
		return err
	}
	// The socket serves the control plane only to the person.
	personServer, err := svc.Server(id)
	if err != nil {
		return err
	}
	sock, err := controlplane.ListenSocket(ctx, filepath.Join(log.Dir(), socketName))
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "pumi-work-")
	if err != nil {
		_ = sock.Close()
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	gw, err := serveGateway(ctx, cfg, out)
	if err != nil {
		_ = sock.Close()
		return err
	}
	if gw != nil {
		defer gw.shutdown(ctx)
	}
	_, _ = fmt.Fprintf(out, "pumi runs the plans of %s; stop it with Ctrl-C.\n%s\n", log.Dir(), gw.describe(cfg))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	parts := []func() error{
		func() error { return sock.Serve(ctx, personServer) },
		func() error {
			return runner.Run(ctx, runner.Config{
				Service: runnerClient, Adapters: cfg.adapters(), Roles: cfg.roleAdapters(),
				CheckAdapter: cfg.file.GetModels().GetCheckAdapter(), Repository: dir, WorkDir: work, Output: out,
				Blobs: blobstore.ForTenant(blobs, embeddedlog.Tenant), Gateway: runnerGateway(gw),
				Models: roleModels(cfg), Fallbacks: fallbacks(cfg), Price: gateway.FigureCost, Maker: gateway.MakerOf,
			})
		},
		func() error {
			return kernel.Run(ctx, kernel.Config{
				Client: kernelClient, Workflow: wf, Base: cfg.file.GetBase(),
				Parallelism: cfg.plane().Settings.Parallelism,
				Trunk:       func(ctx context.Context) (string, error) { return trunkTree(ctx, dir, cfg.file.GetTrunk()) },
				Project:     cfg.project(),
				HasFile:     func(ctx context.Context, tree, path string) (bool, error) { return hasFile(ctx, dir, tree, path) },
			})
		},
	}
	return runAll(cancel, parts)
}

// runAll runs each of parts in its own goroutine, calls cancel when any of
// them returns, and returns the first error that is not a cancel once all
// have returned.
func runAll(cancel context.CancelFunc, parts []func() error) error {
	errs := make(chan error, len(parts))
	for _, part := range parts {
		go func() {
			err := part()
			cancel()
			errs <- err
		}()
	}
	var first error
	for range parts {
		if err := <-errs; first == nil && err != nil && !errors.Is(err, context.Canceled) {
			first = err
		}
	}
	return first
}

// newClients returns the kernel's and the runner's clients of the control
// plane on svc, in-process.
func newClients(svc *controlplane.Service) (controlplanev1connect.ControlPlaneServiceClient, runnerv1connect.RunnerServiceClient, error) {
	k, err := svc.Client(controlplane.Identity{Caller: controlplane.CallerKernel, Principal: kernelPrincipal, Tenant: embeddedlog.Tenant})
	if err != nil {
		return nil, nil, err
	}
	r, err := svc.Client(controlplane.Identity{
		Caller: controlplane.CallerRunner, Principal: runnerPrincipal,
		Roles: []string{controlplane.RoleAgent, controlplane.RoleGate}, Tenant: embeddedlog.Tenant,
	})
	if err != nil {
		return nil, nil, err
	}
	return controlplanev1connect.NewControlPlaneServiceClient(k), runnerv1connect.NewRunnerServiceClient(r), nil
}

// trunkTree returns the tree of the branch trunk of the clone that dir is
// in (decision 0209). git cat-file reads the name on its standard input,
// so its arguments are constant (gosec G204).
func trunkTree(ctx context.Context, dir, trunk string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch-check=%(objectname) %(objecttype)")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("refs/heads/" + trunk + "^{tree}\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git cat-file: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	name, kind, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	if kind != "tree" {
		return "", fmt.Errorf("the clone has no branch %s, the trunk of the configuration file", trunk)
	}
	return name, nil
}

// hasFile reports whether the tree of the clone at dir holds a file at
// path, relative to the tree's root (decision 0250): a blob, not a
// directory or a submodule. A tree that the clone does not have is an
// error. The git arguments are constant; the names go on standard input.
func hasFile(ctx context.Context, dir, tree, path string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch-check=%(objecttype)")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(tree + "^{tree}\n" + tree + ":" + path + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git cat-file: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 || lines[0] != "tree" {
		return false, fmt.Errorf("the clone has no tree %s", tree)
	}
	return lines[1] == "blob", nil
}
