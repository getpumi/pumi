// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
	"getpumi.dev/pumi/internal/gateway"
	"getpumi.dev/pumi/internal/kernel/workflow"
	"getpumi.dev/pumi/internal/runner"
)

// providerURL is the base URL of the provider to which the gateway sends
// the calls. The tests set it to a fake provider.
var providerURL = "https://api.anthropic.com"

// gatewayShutdown is how long pumi run waits for the gateway's calls in
// progress when it stops.
const gatewayShutdown = 5 * time.Second

// modelGateway is the model gateway that pumi run serves on 127.0.0.1, as
// the in-process runner sees it (decision 0231).
type modelGateway struct {
	*gateway.Gateway
	server *gateway.Server
}

// URL is the base URL that a harness is given.
func (m *modelGateway) URL() string { return m.server.URL() }

// Totals returns the usage that the gateway counted for the token's
// invocation.
func (m *modelGateway) Totals(token string) (runner.GatewayTotals, error) {
	t, err := m.Gateway.Totals(token)
	return runner.GatewayTotals{Tokens: t.Tokens, CostMicrodollars: t.CostMicrodollars, CapReached: t.CapReached}, err
}

// shutdown stops serving the gateway, after the calls in progress or
// gatewayShutdown, also when ctx is done.
func (m *modelGateway) shutdown(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayShutdown)
	defer cancel()
	_ = m.server.Shutdown(ctx)
}

// serveGateway serves the model gateway of embedded mode on 127.0.0.1, at
// a port the system picks, in front of providerURL (decision 0231). For an
// adapter in the API key mode, the gateway sends the key of the
// environment variable that its entry names on every call of its
// invocations (decisions 0225 and 0245). An adapter in the sign-in mode
// does not use the gateway: its harness talks to the provider directly
// (decision 0240). serveGateway returns nil, and serves nothing, when no
// adapter is in the API key mode. The gateway logs its refusals and the
// calls it could not pass to log, which never holds a key or a token.
func serveGateway(ctx context.Context, cfg *config, log io.Writer) (*modelGateway, error) {
	if err := checkPricedModels(cfg); err != nil {
		return nil, err
	}
	keys := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(cfg.file.GetModels().GetAdapters())) {
		a := cfg.file.GetModels().GetAdapters()[name]
		if a.GetCredentialMode() != configv1.Adapter_CREDENTIAL_MODE_API_KEY {
			continue
		}
		if keys[name] = os.Getenv(a.GetApiKeyEnv()); keys[name] == "" {
			return nil, fmt.Errorf("the environment variable %s, which the api_key_env of the adapter %q names, is not set or is empty", a.GetApiKeyEnv(), name)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	upstream, err := url.Parse(providerURL)
	if err != nil {
		return nil, fmt.Errorf("the provider's URL %q: %w", providerURL, err)
	}
	g, err := gateway.New(gateway.Config{
		Upstream: upstream, APIKeys: keys, Roles: modelRoles(cfg),
		Log: slog.New(slog.NewTextHandler(log, &slog.HandlerOptions{Level: slog.LevelInfo})),
	})
	if err != nil {
		return nil, err
	}
	server, err := gateway.Serve(ctx, g)
	if err != nil {
		return nil, err
	}
	return &modelGateway{Gateway: g, server: server}, nil
}

// modelRoles returns the model roles of the model configuration as the
// gateway maps them (decision 0233): each role's model, and its fallback's
// model when the fallback is on the same adapter. A fallback on another
// adapter is not a fallback within a run (decision 0246).
func modelRoles(cfg *config) map[string]gateway.Role {
	roles := map[string]gateway.Role{}
	for name, r := range cfg.file.GetModels().GetRoles() {
		role := gateway.Role{Model: r.GetModel()}
		if f := r.GetFallback(); f.GetAdapter() == r.GetAdapter() {
			role.Fallback = f.GetModel()
		}
		roles[name] = role
	}
	return roles
}

// checkPricedModels refuses a model configuration whose role names a model,
// or a fallback model on the same adapter, that the list-price table does
// not list, whichever path its adapter takes: the gateway refuses the calls
// of such a model, and the runner cannot price its usage figures on the
// sign-in path (decisions 0229 and 0241).
func checkPricedModels(cfg *config) error {
	roles := cfg.file.GetModels().GetRoles()
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(roles)) {
		r := roles[name]
		models := []string{r.GetModel()}
		if f := r.GetFallback(); f.GetAdapter() == r.GetAdapter() && f.GetModel() != "" {
			models = append(models, f.GetModel())
		}
		for _, m := range models {
			if _, ok := gateway.Price(m); !ok {
				errs = append(errs, fmt.Errorf("the model role %q names the model %q, which the list-price table (version %s) does not list", name, m, gateway.PricesVersion))
			}
		}
	}
	return errors.Join(errs...)
}

// roleModels returns the models of each model role for an adapter in the
// sign-in mode: the role's model, and its fallback's model when the
// fallback is on the same adapter (decisions 0246 and 0254).
func roleModels(cfg *config) map[string]runner.Models {
	out := map[string]runner.Models{}
	for name, r := range modelRoles(cfg) {
		out[name] = runner.Models{Model: r.Model, Fallback: r.Fallback}
	}
	return out
}

// runnerGateway returns the gateway as the runner sees it: nil when pumi
// run serves none.
func runnerGateway(m *modelGateway) runner.Gateway {
	if m == nil {
		return nil
	}
	return m
}

// checkModelRoles refuses a workflow that names a model role which the
// configuration file does not map (decision 0233).
func checkModelRoles(cfg *config, wf *workflow.Workflow) error {
	roles := cfg.file.GetModels().GetRoles()
	var missing []string
	for _, r := range wf.ModelRoles() {
		if _, ok := roles[r]; !ok {
			missing = append(missing, strconv.Quote(r))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the workflow names the model roles %s, which models.roles of the configuration file does not map", strings.Join(missing, ", "))
	}
	return nil
}

// describe says where the gateway serves, when it serves, and which
// credential reaches the provider for each adapter. m may be nil.
func (m *modelGateway) describe(cfg *config) string {
	adapters := cfg.file.GetModels().GetAdapters()
	var each []string
	for _, name := range slices.Sorted(maps.Keys(adapters)) {
		if adapters[name].GetCredentialMode() == configv1.Adapter_CREDENTIAL_MODE_SIGN_IN {
			each = append(each, fmt.Sprintf("the harness of %s talks to the provider directly with the person's own sign-in", name))
		} else {
			each = append(each, fmt.Sprintf("for %s it sends the API key of %s", name, adapters[name].GetApiKeyEnv()))
		}
	}
	if m == nil {
		return fmt.Sprintf("No model gateway runs: %s.", strings.Join(each, "; "))
	}
	return fmt.Sprintf("The model gateway serves at %s; %s.", m.URL(), strings.Join(each, "; "))
}
