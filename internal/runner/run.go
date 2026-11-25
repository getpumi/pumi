// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"getpumi.dev/pumi/api"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The defaults of Config.
const (
	// DefaultGrace is how long an adapter has to stop after the runner
	// closes its standard input, before the runner kills it (decision
	// 0035).
	DefaultGrace = 30 * time.Second
	// DefaultLogInterval is how often the runner sends a batch of the
	// adapter's log output (decision 0163).
	DefaultLogInterval = time.Second
)

// runnerPrefix starts each line that the runner adds to an invocation's
// log, such as the reason it failed the invocation.
const runnerPrefix = "pumi runner: "

// protocolVersions are the versions of the runner protocol that the runner
// supports.
var protocolVersions = []*runnerv1.ProtocolVersion{{Major: 1, Minor: 0}}

// Config is what the wiring gives the in-process runner, which runs one
// harness adapter (decision 0180).
type Config struct {
	// Service is the client of RunnerService, through which the runner
	// claims invocations and reports their results (decision 0152).
	Service runnerv1connect.RunnerServiceClient
	// Adapters are the harness adapters by name (decision 0245). The
	// runner starts one of them once for each invocation (decision 0158):
	// the adapter of the model role of an agent run, and CheckAdapter for
	// a check or an external action.
	Adapters map[string]Adapter
	// Roles maps each model role, such as "frontier", to the name of its
	// adapter (decision 0245).
	Roles map[string]string
	// CheckAdapter is the name of the adapter that runs the checks and
	// external actions, which name no model role (decision 0253).
	CheckAdapter string
	// Remote is set when the runner runs in remote mode, which refuses an
	// adapter in the sign-in mode (decision 0240).
	Remote bool
	// Repository is a directory in the git repository that holds the
	// invocations' input trees and receives their resulting trees.
	Repository string
	// WorkDir is the directory in which the runner makes a new directory
	// for each invocation, with its workspace, and removes it after the
	// invocation.
	WorkDir string
	// Grace is how long an adapter has to stop after the runner closes its
	// standard input; DefaultGrace when zero.
	Grace time.Duration
	// LogInterval is how often the runner sends a batch of the adapter's
	// standard error; DefaultLogInterval when zero.
	LogInterval time.Duration
	// Blobs is the blob store of the runner's tenant, to which the runner
	// uploads the output files that the adapter reports (decisions 0033
	// and 0160).
	Blobs Blobs
	// Output, when set, also receives each invocation's log, masked, as it
	// is written, each line after the invocation's id and ": ", so that
	// pumi run prints it (getpumi/pumi#88).
	Output io.Writer
	// Gateway is the model gateway, through which every agent run's model
	// calls go. The runner refuses an agent run when it is nil (decisions
	// 0226 and 0231).
	Gateway Gateway
}

// Adapter is a harness adapter's executable and how its harness reaches
// the provider (decisions 0240 and 0245).
type Adapter struct {
	// Path is the absolute path of the executable.
	Path string
	// SHA256 is the executable's SHA-256 in lowercase hexadecimal. The
	// runner refuses to start an executable whose SHA-256 differs.
	SHA256 string
	// SignIn is set when the harness uses the person's own sign-in, and
	// not an API key that the gateway holds.
	SignIn bool
}

// Gateway is the model gateway as the runner sees it (decisions 0225,
// 0230 and 0231).
type Gateway interface {
	// URL is the base URL that a harness is given.
	URL() string
	// KeepsSignIn reports whether the harness of the adapter, by name,
	// keeps its own sign-in, which is when the gateway holds no API key
	// for it (decisions 0225 and 0240).
	KeepsSignIn(adapter string) bool
	// Open opens the invocation, which the adapter runs, with its budget
	// and returns a random token that is valid for it until Close. The
	// gateway writes each fallback and retry of the invocation's calls to
	// log, the invocation's log (decision 0233).
	Open(invocation, adapter string, budget *harnessv1.Usage, log io.Writer) (string, error)
	// Totals returns the usage that the gateway counted for the token's
	// invocation.
	Totals(token string) (GatewayTotals, error)
	// Close ends the token.
	Close(token string) error
}

// GatewayTotals are an invocation's usage as the gateway counted it.
type GatewayTotals struct {
	// Tokens are all the tokens of every call (decision 0227).
	Tokens int64
	// CostMicrodollars is the reference cost at the list prices (decision
	// 0229).
	CostMicrodollars int64
	// CapReached is set when the gateway refused a call of the invocation
	// because it had reached its token or cost cap (decision 0228).
	CapReached bool
}

// Blobs is the blob store as the runner sees it: bound to the runner's
// tenant, which the runner never names (decision 0039).
type Blobs interface {
	// Put stores the content read from content and returns its hash,
	// "sha256:" and the hexadecimal SHA-256.
	Put(ctx context.Context, content io.Reader) (string, error)
}

// check refuses a configuration that lacks a value the runner needs, and
// sets the defaults.
func (c *Config) check() error {
	switch {
	case c.Service == nil:
		return errors.New("runner: no RunnerService client")
	case c.Repository == "":
		return errors.New("runner: no repository")
	case c.WorkDir == "":
		return errors.New("runner: no work directory")
	case c.Blobs == nil:
		return errors.New("runner: no blob store")
	}
	if err := c.checkAdapters(); err != nil {
		return err
	}
	if c.Grace <= 0 {
		c.Grace = DefaultGrace
	}
	if c.LogInterval <= 0 {
		c.LogInterval = DefaultLogInterval
	}
	return nil
}

// checkAdapters refuses a configuration with no adapter, with an adapter
// whose path is not absolute or that has no SHA-256, with a check adapter
// or a model role whose adapter it does not list, and, in remote mode,
// with an adapter in the sign-in mode (decisions 0240, 0245 and 0253).
func (c *Config) checkAdapters() error {
	if len(c.Adapters) == 0 {
		return errors.New("runner: no adapter")
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(c.Adapters)) {
		a := c.Adapters[name]
		switch {
		case !filepath.IsAbs(a.Path):
			errs = append(errs, fmt.Errorf("runner: the adapter %q has the path %q, which is not absolute", name, a.Path))
		case !validSHA256(a.SHA256):
			errs = append(errs, fmt.Errorf("runner: the adapter %q has the SHA-256 %q, which is not 64 lowercase hexadecimal digits", name, a.SHA256))
		case c.Remote && a.SignIn:
			errs = append(errs, fmt.Errorf("runner: remote mode refuses the adapter %q, which is in the sign-in mode; remote mode uses API keys only (decision 0240)", name))
		}
	}
	if _, ok := c.Adapters[c.CheckAdapter]; !ok {
		errs = append(errs, fmt.Errorf("runner: the check adapter %q is not one of the adapters (decision 0253)", c.CheckAdapter))
	}
	for _, role := range slices.Sorted(maps.Keys(c.Roles)) {
		if _, ok := c.Adapters[c.Roles[role]]; !ok {
			errs = append(errs, fmt.Errorf("runner: the model role %q names the adapter %q, which is not one of the adapters", role, c.Roles[role]))
		}
	}
	return errors.Join(errs...)
}

// validSHA256 reports whether s is a SHA-256 in lowercase hexadecimal.
func validSHA256(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Run claims and runs invocations, one at a time, until ctx is done or a
// call of RunnerService fails, and returns the error that ended it.
func Run(ctx context.Context, cfg Config) error {
	for ctx.Err() == nil {
		if _, err := RunOne(ctx, cfg); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
	return ctx.Err()
}

// RunOne claims an invocation through RunnerService, runs it and reports
// its result. It reports whether it claimed one: Claim is a long poll,
// which ends with none when no invocation was ready.
//
// The runner checks the input tree out into a new workspace, starts the
// adapter there with the harness invocation as one line on its standard
// input, sends heartbeats while it runs and batches of its standard error
// as the log, and reads its standard output as lines: the usage figures
// that the adapter passes while it runs, which the runner writes to the log
// as they arrive, and the result as the last line (decisions 0158, 0241
// and 0252). The refusals of the result go into invocation.result, and a
// result with the status limited fails the invocation with the limit as
// its reason in the log (decision 0249). It parses the structured output strictly into the message that
// the document names, computes the resulting tree from the workspace and
// fails the invocation when a file was written outside the allowed paths,
// before the result is appended (decisions 0159, 0160 and 0162). It then
// uploads each output file that the adapter reports to the blob store and
// records its path and hash in the result; a path outside the workspace
// fails the invocation (decision 0160).
//
// The runner masks the values of the credentials it holds for the
// invocation, the gateway token included, in the log batches, in Output
// and in the full log, which it stores in the blob store and records in
// the result by its hash, whatever the outcome (decisions 0034, 0163 and
// 0232).
//
// Before it starts the adapter of an agent run, the runner opens the
// invocation with the model gateway and passes the gateway's base URL, the
// token and whether the harness keeps its own sign-in in the harness
// invocation; after the adapter exits, it reads the gateway's totals and
// closes the token. It refuses an agent run when it has no gateway, or
// when the run's network access does not reach the gateway (decisions
// 0226 and 0231). The usage of the result is the gateway's totals, none
// for a check or an external action, with the wall time measured from the
// start of the adapter; the adapter's own figures go to the log (decision
// 0230). When the gateway refused a call of the invocation at its token or
// cost cap, the outcome is cap reached, whatever the adapter reports
// (decision 0228).
//
// When a heartbeat carries a cancel request, the runner closes the
// adapter's standard input, kills it after the grace period and reports
// the outcome cancelled (decision 0035). When the wall time reaches the
// budget's seconds, it stops the adapter the same way and reports the
// outcome cap reached (decisions 0193 and 0204). When ctx is done, or a call of RunnerService fails while the
// adapter runs, it stops the adapter the same way, reports nothing and
// returns the error: an invocation with no result is in doubt (decisions
// 0182 and 0199). A call that cannot reach the control plane ends the
// invocation only once the lease would have ended, so the wall-time limit
// holds meanwhile (decision 0205).
func RunOne(ctx context.Context, cfg Config) (bool, error) {
	if err := cfg.check(); err != nil {
		return false, err
	}
	repo, err := openRepository(ctx, cfg.Repository)
	if err != nil {
		return false, err
	}
	// The lease is counted from before the claim, so that the runner never
	// takes it to end later than the control plane does (decision 0205).
	claimed := time.Now()
	claim, err := cfg.Service.Claim(ctx, &runnerv1.ClaimRequest{ProtocolVersions: protocolVersions})
	if err != nil {
		return false, fmt.Errorf("runner: the claim: %w", err)
	}
	if claim.GetInvocation() == nil {
		return false, nil
	}
	log := &logBuffer{}
	if cfg.Output != nil {
		log.out = &lineWriter{out: cfg.Output, prefix: claim.GetInvocation().GetInvocation() + ": "}
		defer log.flush()
	}
	r := &run{cfg: cfg, repo: repo, claim: claim, claimed: claimed, doc: claim.GetInvocation(), log: log}
	return true, r.do(ctx)
}

// run is one claimed invocation.
type run struct {
	cfg   Config
	repo  repository
	claim *runnerv1.ClaimResponse
	// claimed is when the runner asked for the claim.
	claimed time.Time
	doc     *runnerv1.Invocation
	log     *logBuffer
	// token is the invocation's gateway token while it is open.
	token string
	// adapter is the name of the adapter that runs the invocation, once
	// the runner picked it.
	adapter string
}

// do runs the invocation in a new directory, which it removes after, and
// reports its result.
func (r *run) do(ctx context.Context) error {
	dir, err := os.MkdirTemp(r.cfg.WorkDir, "invocation-")
	if err != nil {
		return fmt.Errorf("runner: the directory of %s: %w", r.doc.GetInvocation(), err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	work, index := filepath.Join(dir, "workspace"), filepath.Join(dir, "index")
	result := &eventsv1.InvocationResult{Invocation: r.doc.GetInvocation()}
	ex, mt, err := r.start(ctx, work, index)
	// The adapter has exited, or never started: the runner reads the
	// gateway's totals and ends the token (decisions 0230 and 0231).
	totals, gerr := r.closeGateway()
	if err != nil && ctx.Err() != nil {
		// The runner stopped before the adapter started, as while it
		// checked the input tree out: like a stop while the adapter runs,
		// that ends the invocation without a report (decision 0182).
		return ctx.Err()
	}
	if err != nil {
		return r.report(ctx, result, eventsv1.InvocationFinished_OUTCOME_FAILED, err)
	}
	if ex.ended != nil {
		return ex.ended
	}
	if gerr != nil {
		return r.report(ctx, result, eventsv1.InvocationFinished_OUTCOME_FAILED, gerr)
	}
	outcome, err := r.judge(ctx, ex, mt, work, index, totals, result)
	return r.report(ctx, result, outcome, err)
}

// openGateway opens an agent run with the model gateway, with its budget,
// and returns how the harness reaches the gateway; the log masks the token
// (decisions 0226, 0231 and 0232). It returns nil for any other kind of
// invocation, which makes no model calls. It refuses an agent run that
// checkAgentRun refuses.
func (r *run) openGateway() (*harnessv1.Gateway, error) {
	if err := checkAgentRun(r.doc, r.cfg.Gateway != nil); err != nil {
		return nil, err
	}
	if r.doc.GetComponent().GetKind() != harnessv1.Component_KIND_AGENT_RUN {
		return nil, nil
	}
	token, err := r.cfg.Gateway.Open(r.doc.GetInvocation(), r.adapter, r.doc.GetBudget(), r.log)
	if err != nil {
		return nil, fmt.Errorf("the model gateway could not open the invocation: %w", err)
	}
	r.token = token
	r.log.mask(token)
	return &harnessv1.Gateway{BaseUrl: r.cfg.Gateway.URL(), Token: token, KeepsSignIn: r.cfg.Gateway.KeepsSignIn(r.adapter)}, nil
}

// closeGateway reads the totals of the open token and closes it. It
// returns no totals when no token is open.
func (r *run) closeGateway() (GatewayTotals, error) {
	if r.token == "" {
		return GatewayTotals{}, nil
	}
	token := r.token
	r.token = ""
	totals, err := r.cfg.Gateway.Totals(token)
	if cerr := r.cfg.Gateway.Close(token); cerr != nil {
		r.log.note("the model gateway could not close the token of %s: %v", r.doc.GetInvocation(), cerr)
	}
	if err != nil {
		return GatewayTotals{}, fmt.Errorf("the model gateway's totals could not be read: %w", err)
	}
	return totals, nil
}

// start checks the input tree out into work, finds the output message and
// its JSON Schema, and runs the adapter. An error fails the invocation
// before the adapter starts.
func (r *run) start(ctx context.Context, work, index string) (*execution, protoreflect.MessageType, error) {
	adapter, err := r.pickAdapter()
	if err != nil {
		return nil, nil, err
	}
	gateway, err := r.openGateway()
	if err != nil {
		return nil, nil, err
	}
	if err := r.repo.checkout(ctx, r.doc.GetInput().GetTree(), work, index); err != nil {
		return nil, nil, fmt.Errorf("the input tree could not be checked out: %w", err)
	}
	var mt protoreflect.MessageType
	resolved := Resolved{Workspace: work, Gateway: gateway}
	if name := r.doc.GetOutputMessage(); name != "" {
		var err error
		if mt, resolved.JSONSchema, err = api.Output(name); err != nil {
			return nil, nil, err
		}
	}
	inv, err := HarnessInvocation(r.doc, resolved)
	if err != nil {
		return nil, nil, err
	}
	ex, err := r.execute(ctx, adapter.Path, inv)
	return ex, mt, err
}

// pickAdapter picks the adapter of the invocation: the adapter of the
// model role of an agent run, or the check adapter for a check or an
// external action (decisions 0245 and 0253). It checks the executable's
// SHA-256 and refuses one that does not match, and writes the adapter's
// name, path and SHA-256 to the log (decision 0245).
func (r *run) pickAdapter() (Adapter, error) {
	name := r.cfg.CheckAdapter
	if r.doc.GetComponent().GetKind() == harnessv1.Component_KIND_AGENT_RUN {
		var ok bool
		if name, ok = r.cfg.Roles[r.doc.GetModelRole()]; !ok {
			return Adapter{}, fmt.Errorf("the model role %q of the agent run has no adapter (decision 0245)", r.doc.GetModelRole())
		}
	}
	a := r.cfg.Adapters[name]
	got, err := hashFile(a.Path)
	if err != nil {
		return Adapter{}, fmt.Errorf("the SHA-256 of the adapter %s (%s) could not be computed: %w", name, a.Path, err)
	}
	if got != a.SHA256 {
		return Adapter{}, fmt.Errorf("the adapter %s (%s) has the SHA-256 %s, but the configuration pins %s; the runner does not start it (decision 0245)", name, a.Path, got, a.SHA256)
	}
	r.adapter = name
	r.log.note("the adapter %s (%s, SHA-256 %s) runs the invocation %s", name, a.Path, got, r.doc.GetInvocation())
	return a, nil
}

// hashFile returns the SHA-256 of the file at path, in lowercase
// hexadecimal, after following its links, as starting it does. The file is
// read through a root of its directory, because gosec reports a path built
// from a variable (G304).
func hashFile(path string) (string, error) {
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(filepath.Base(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// report adds the reason the invocation failed to the log when problem is
// not nil, stores the full log in the blob store and records its hash in
// result, sends the remaining log and reports the result with the outcome
// (decisions 0034 and 0232). When the log cannot be stored, the result has
// no log hash, the log sent says why, and the outcome stands.
func (r *run) report(ctx context.Context, result *eventsv1.InvocationResult, outcome eventsv1.InvocationFinished_Outcome, problem error) error {
	if problem != nil {
		r.log.note("the invocation %s failed: %v", r.doc.GetInvocation(), problem)
	}
	r.log.end()
	hash, err := r.cfg.Blobs.Put(ctx, bytes.NewReader(r.log.full()))
	if err != nil {
		r.log.note("the log of %s could not be stored: %v", r.doc.GetInvocation(), err)
		r.log.end()
	}
	result.LogBlobHash = hash
	if err := r.sendLog(ctx); err != nil {
		return err
	}
	_, err = r.cfg.Service.Report(ctx, &runnerv1.ReportRequest{
		Invocation: r.doc.GetInvocation(), LeaseNumber: r.claim.GetLeaseNumber(), Result: result, Outcome: outcome,
	})
	if err != nil {
		return fmt.Errorf("runner: the report of %s: %w", r.doc.GetInvocation(), err)
	}
	return nil
}

// sendLog sends the log output that has not been sent.
func (r *run) sendLog(ctx context.Context) error {
	out := r.log.take()
	if len(out) == 0 {
		return nil
	}
	_, err := r.cfg.Service.SendLog(ctx, &runnerv1.SendLogRequest{
		Invocation: r.doc.GetInvocation(), LeaseNumber: r.claim.GetLeaseNumber(), Output: out,
	})
	if err != nil {
		// The batch goes again with the next one.
		r.log.restore(out)
		return fmt.Errorf("runner: a log batch of %s: %w", r.doc.GetInvocation(), err)
	}
	return nil
}
