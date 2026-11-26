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
	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	"getpumi.dev/pumi/api/gen/pumi/runner/v1/runnerv1connect"
	"google.golang.org/protobuf/proto"
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
	// Models maps each model role to the models that an adapter in the
	// sign-in mode gets in the harness invocation (decision 0254).
	Models map[string]Models
	// Fallbacks maps each model role whose fallback is on another adapter
	// to that adapter and model. When the provider's limit stops a run of
	// the role, the runner starts the fallback's adapter once, and the
	// role stays on it until a person switches it back (decision 0246).
	Fallbacks map[string]Fallback
	// Price prices a usage figure that an adapter in the sign-in mode
	// passes with the gateway's list-price table, in picodollars, and
	// reports whether the table lists its model (decision 0241). The runner
	// refuses an adapter in the sign-in mode without it.
	Price func(*harnessv1.UsageFigure) (picodollars int64, listed bool)
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
	// ReadPaths are absolute paths outside the workspace that the commands
	// of the adapter's harness may read, such as a toolchain; the runner
	// passes them in each invocation (decision 0269).
	ReadPaths []string
}

// Models are the models that the configuration maps a model role to, for
// an adapter in the sign-in mode (decision 0254).
type Models struct {
	// Model is the role's model.
	Model string
	// Fallback is the model of the role's fallback when the fallback is on
	// the same adapter; empty otherwise (decision 0246).
	Fallback string
}

// Fallback is the adapter and model of a model role's fallback on another
// adapter (decision 0246).
type Fallback struct {
	// Adapter is the name of the adapter.
	Adapter string
	// Model is the model.
	Model string
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
// or a model role whose adapter it does not list, with an adapter in the
// sign-in mode when it has no price table, and, in remote mode, with an
// adapter in the sign-in mode (decisions 0240, 0241, 0245 and 0253).
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
		case a.SignIn && c.Price == nil:
			errs = append(errs, fmt.Errorf("runner: the adapter %q is in the sign-in mode, and the runner has no price table to price its usage figures (decision 0241)", name))
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
	for _, role := range slices.Sorted(maps.Keys(c.Fallbacks)) {
		if _, ok := c.Adapters[c.Fallbacks[role].Adapter]; !ok {
			errs = append(errs, fmt.Errorf("runner: the fallback of the model role %q names the adapter %q, which is not one of the adapters", role, c.Fallbacks[role].Adapter))
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
// An agent run whose adapter is in the sign-in mode has no gateway: its
// harness talks to the provider directly, and the harness invocation
// carries the models that the configuration maps its role to (decisions
// 0240 and 0254). The runner counts the usage figures that the adapter
// passes, prices them with the Price of the configuration, and stops the
// adapter as for a cancel once the tokens or cost reach the budget, which
// is cap reached; a figure whose model the price table does not list
// stops it too and fails the invocation. The usage of the result is the
// runner's count with the measured wall time, or, when the adapter passed
// no figures, the usage of its result, whose caps are checked when the run
// ends (decision 0241).
//
// When the provider's limit stops a run of an agent run's model role and
// the role has a fallback on another adapter, the runner starts the
// fallback's adapter once in a fresh checkout of the same input tree, with
// what the first run left of the budget, and reports that run's result
// with the usage of both and the switch of the role; when the claim says
// that the role is on its fallback, it starts only the fallback's adapter
// (decision 0246).
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
	// meter counts the usage figures of an agent run on the sign-in path;
	// nil on any other (decision 0241).
	meter *meter
	// budget is what the current run of an adapter may use: the
	// document's budget, or what the first run left for the run of the
	// fallback (decision 0246).
	budget *harnessv1.Usage
	// onFallback is set while the invocation runs on its model role's
	// fallback on another adapter (decision 0246).
	onFallback bool
}

// do runs the invocation and reports its result. When the provider's
// limit stops a run of an agent run's model role on its own adapter, and
// the role has a fallback on another adapter, the runner starts the
// fallback's adapter once, in a fresh checkout of the same input tree,
// with what the first run left of the budget, and reports that run's
// result with both runs' usage and the switch of the role; both runs are
// in the one log. When the claim says that the role is already on its
// fallback, the runner starts only the fallback's adapter, and a limit
// fails the invocation (decision 0246).
func (r *run) do(ctx context.Context) error {
	r.budget = r.doc.GetBudget()
	role := r.doc.GetModelRole()
	fallback, hasFallback := r.cfg.Fallbacks[role]
	hasFallback = hasFallback && r.doc.GetComponent().GetKind() == harnessv1.Component_KIND_AGENT_RUN
	if r.claim.GetModelRoleSwitched() {
		if !hasFallback {
			return r.report(ctx, &eventsv1.InvocationResult{Invocation: r.doc.GetInvocation()}, eventsv1.InvocationFinished_OUTCOME_FAILED,
				fmt.Errorf("the log says that the model role %q is on its fallback, but the configuration gives it no fallback on another adapter; the runner does not switch the role back by itself, and a person does with pumi role reset %s (decision 0246)", role, role))
		}
		r.onFallback = true
		r.log.note("the model role %s is on its fallback, the model %s on the adapter %s, since a limit switched it; pumi role reset %s switches it back (decision 0246)",
			role, fallback.Model, fallback.Adapter, role)
	}
	first := r.attempt(ctx)
	if first.ended != nil {
		return first.ended
	}
	if !first.limited || r.onFallback || !hasFallback {
		return r.report(ctx, first.result, first.outcome, first.problem)
	}
	switched, err := r.switchEvent(fallback)
	if err != nil {
		return r.report(ctx, first.result, eventsv1.InvocationFinished_OUTCOME_FAILED, err)
	}
	left, ok := remaining(r.budget, first.result.GetUsage())
	if !ok {
		r.log.note("the model role %s switches to its fallback, the model %s on the adapter %s, but the first run left none of the budget for it (decision 0246)",
			role, fallback.Model, fallback.Adapter)
		return r.report(ctx, first.result, first.outcome, first.problem, switched)
	}
	r.log.note("the model role %s switches to its fallback, the model %s on the adapter %s, after the provider's limit stopped its run on the adapter %s; the fallback runs once in a fresh checkout of the input tree with %d tokens, %d microdollars and %d s of the budget left, and the role stays on it until pumi role reset %s (decision 0246)",
		role, fallback.Model, fallback.Adapter, r.adapter, left.GetTokens(), left.GetCostMicrodollars(), left.GetSeconds(), role)
	r.onFallback, r.budget = true, left
	second := r.attempt(ctx)
	if second.ended != nil {
		return second.ended
	}
	second.result.Usage = addUsage(first.result.GetUsage(), second.result.GetUsage())
	return r.report(ctx, second.result, second.outcome, second.problem, switched)
}

// attempt is how one run of an adapter for the invocation ended.
type attempt struct {
	result  *eventsv1.InvocationResult
	outcome eventsv1.InvocationFinished_Outcome
	// problem is the reason the invocation failed, when it did.
	problem error
	// ended is the error that ends the invocation without a report.
	ended error
	// limited is set when the provider's usage or rate limit stopped the
	// run, which the runner did not stop and whose caps were not reached
	// (decision 0249).
	limited bool
}

// attempt runs the adapter once in a new directory, which it removes
// after, and judges how it ended.
func (r *run) attempt(ctx context.Context) attempt {
	result := &eventsv1.InvocationResult{Invocation: r.doc.GetInvocation()}
	r.adapter, r.meter = "", nil
	dir, err := os.MkdirTemp(r.cfg.WorkDir, "invocation-")
	if err != nil {
		return attempt{ended: fmt.Errorf("runner: the directory of %s: %w", r.doc.GetInvocation(), err)}
	}
	defer func() { _ = os.RemoveAll(dir) }()
	work, index := filepath.Join(dir, "workspace"), filepath.Join(dir, "index")
	ex, mt, err := r.start(ctx, work, index)
	// The adapter has exited, or never started: the runner reads the
	// gateway's totals and ends the token (decisions 0230 and 0231).
	totals, gerr := r.closeGateway()
	switch {
	case err != nil && ctx.Err() != nil:
		// The runner stopped before the adapter started, as while it
		// checked the input tree out: like a stop while the adapter runs,
		// that ends the invocation without a report (decision 0182).
		return attempt{ended: ctx.Err()}
	case err != nil:
		return attempt{result: result, outcome: eventsv1.InvocationFinished_OUTCOME_FAILED, problem: err}
	case ex.ended != nil:
		return attempt{ended: ex.ended}
	case gerr != nil:
		return attempt{result: result, outcome: eventsv1.InvocationFinished_OUTCOME_FAILED, problem: gerr}
	}
	outcome, err := r.judge(ctx, ex, mt, work, index, totals, result)
	limited := ex.result.GetStatus() == harnessv1.Result_STATUS_LIMITED && ex.waitErr == nil && ex.readErr == nil &&
		!ex.cancelled && !ex.capped && outcome == eventsv1.InvocationFinished_OUTCOME_FAILED
	return attempt{result: result, outcome: outcome, problem: err, limited: limited}
}

// switchEvent returns the model_role.switched of the invocation's model
// role to fallback, which names the invocation and the limit: the adapter
// that the limit stopped and the role's own model (decision 0246). The
// control plane sets the tenant from the runner's identity.
func (r *run) switchEvent(fallback Fallback) (*cloudeventsv1.CloudEvent, error) {
	role := r.doc.GetModelRole()
	e, err := events.New(&eventsv1.ModelRoleSwitched{
		Role: role, Adapter: fallback.Adapter, Model: fallback.Model, Invocation: r.doc.GetInvocation(),
		Limit: &eventsv1.ModelRoleSwitched_Limit{Adapter: r.adapter, Model: r.cfg.Models[role].Model},
	}, events.Attributes{
		ID: r.doc.GetInvocation() + "/model-role-switched", Source: switchSource, Subject: role, Tenant: switchTenant,
	})
	if err != nil {
		return nil, fmt.Errorf("the switch of the model role %s could not be written: %w", role, err)
	}
	return e, nil
}

// The source of the switch that the runner reports, and the tenant that it
// names until the control plane replaces it with the runner's own
// (decision 0039).
const (
	switchSource = "/runner"
	switchTenant = "runner"
)

// remaining returns what budget leaves after used, and false when used
// reached any of its caps. A cap of zero sets none and stays zero.
func remaining(budget *harnessv1.Usage, used *eventsv1.InvocationResult_Usage) (*harnessv1.Usage, bool) {
	left := &harnessv1.Usage{}
	ok := true
	less := func(limit, spent int64) int64 {
		if limit <= 0 {
			return 0
		}
		if spent >= limit {
			ok = false
		}
		return limit - spent
	}
	left.Tokens = less(budget.GetTokens(), used.GetTokens())
	left.CostMicrodollars = less(budget.GetCostMicrodollars(), used.GetCostMicrodollars())
	left.Seconds = less(budget.GetSeconds(), used.GetSeconds())
	return left, ok
}

// addUsage returns the sum of a and b.
func addUsage(a, b *eventsv1.InvocationResult_Usage) *eventsv1.InvocationResult_Usage {
	return &eventsv1.InvocationResult_Usage{
		Tokens:           a.GetTokens() + b.GetTokens(),
		CostMicrodollars: a.GetCostMicrodollars() + b.GetCostMicrodollars(),
		Seconds:          a.GetSeconds() + b.GetSeconds(),
	}
}

// openGateway opens an agent run whose adapter is in the API key mode with
// the model gateway, with its budget, and returns how the harness reaches
// the gateway; the log masks the token (decisions 0226, 0231 and 0232). It
// returns nil for an agent run on the sign-in path, whose harness talks to
// the provider directly and whose usage figures the runner counts instead
// (decisions 0240 and 0241), and for any other kind of invocation, which
// makes no model calls. It refuses an agent run that checkAgentRun refuses.
func (r *run) openGateway(adapter Adapter) (*harnessv1.Gateway, error) {
	if err := checkAgentRun(r.doc, adapter.SignIn, r.cfg.Gateway != nil && !adapter.SignIn); err != nil {
		return nil, err
	}
	if r.doc.GetComponent().GetKind() != harnessv1.Component_KIND_AGENT_RUN {
		return nil, nil
	}
	if adapter.SignIn {
		r.meter = newMeter(r.budget, r.cfg.Price)
		return nil, nil
	}
	token, err := r.cfg.Gateway.Open(r.doc.GetInvocation(), r.adapter, r.budget, r.log)
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
	gateway, err := r.openGateway(adapter)
	if err != nil {
		return nil, nil, err
	}
	if err := r.repo.checkout(ctx, r.doc.GetInput().GetTree(), work, index); err != nil {
		return nil, nil, fmt.Errorf("the input tree could not be checked out: %w", err)
	}
	var mt protoreflect.MessageType
	resolved := Resolved{Workspace: work, Gateway: gateway, ReadPaths: adapter.ReadPaths}
	if r.meter != nil {
		models := r.cfg.Models[r.doc.GetModelRole()]
		if r.onFallback {
			// The fallback on another adapter has no fallback of its own
			// (decision 0246).
			models = Models{Model: r.cfg.Fallbacks[r.doc.GetModelRole()].Model}
		}
		resolved.SignIn, resolved.Model, resolved.FallbackModel = true, models.Model, models.Fallback
	}
	if name := r.doc.GetOutputMessage(); name != "" {
		var err error
		if mt, resolved.JSONSchema, err = api.Output(name); err != nil {
			return nil, nil, err
		}
	}
	doc := r.doc
	if !proto.Equal(doc.GetBudget(), r.budget) {
		doc = proto.CloneOf(r.doc)
		doc.Budget = proto.CloneOf(r.budget)
	}
	inv, err := HarnessInvocation(doc, resolved)
	if err != nil {
		return nil, nil, err
	}
	ex, err := r.execute(ctx, adapter.Path, inv)
	return ex, mt, err
}

// pickAdapter picks the adapter of the invocation: the adapter of the
// model role of an agent run, or of its fallback when the role is on it,
// or the check adapter for a check or an external action (decisions 0245,
// 0246 and 0253). It checks the executable's
// SHA-256 and refuses one that does not match, and writes the adapter's
// name, path and SHA-256 to the log (decision 0245).
func (r *run) pickAdapter() (Adapter, error) {
	name := r.cfg.CheckAdapter
	if r.doc.GetComponent().GetKind() == harnessv1.Component_KIND_AGENT_RUN {
		var ok bool
		if name, ok = r.cfg.Roles[r.doc.GetModelRole()]; !ok {
			return Adapter{}, fmt.Errorf("the model role %q of the agent run has no adapter (decision 0245)", r.doc.GetModelRole())
		}
		if r.onFallback {
			name = r.cfg.Fallbacks[r.doc.GetModelRole()].Adapter
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
// and the events evs (decisions 0034 and 0232). When the log cannot be stored, the result has
// no log hash, the log sent says why, and the outcome stands.
func (r *run) report(ctx context.Context, result *eventsv1.InvocationResult, outcome eventsv1.InvocationFinished_Outcome, problem error, evs ...*cloudeventsv1.CloudEvent) error {
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
		Invocation: r.doc.GetInvocation(), LeaseNumber: r.claim.GetLeaseNumber(), Result: result, Outcome: outcome, Events: evs,
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
