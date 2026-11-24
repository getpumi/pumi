// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

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
// a port the system picks, in front of providerURL (decision 0231). With
// the API key of the environment variable that the configuration file
// names, the gateway sends that key on every call; with none, the harness
// keeps its own sign-in (decision 0225). The gateway logs its refusals and
// the calls it could not pass to log, which never holds a key or a token.
func serveGateway(ctx context.Context, cfg *config, log io.Writer) (*modelGateway, error) {
	var key string
	if name := cfg.file.GetGateway().GetApiKeyEnv(); name != "" {
		if key = os.Getenv(name); key == "" {
			return nil, fmt.Errorf("the environment variable %s, which gateway.api_key_env of the configuration file names, is not set or is empty", name)
		}
	}
	upstream, err := url.Parse(providerURL)
	if err != nil {
		return nil, fmt.Errorf("the provider's URL %q: %w", providerURL, err)
	}
	g, err := gateway.New(gateway.Config{
		Upstream: upstream, APIKey: key, Roles: modelRoles(cfg),
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

// modelRoles returns the model roles of the configuration file (decision
// 0233).
func modelRoles(cfg *config) map[string]gateway.Role {
	roles := map[string]gateway.Role{}
	for name, r := range cfg.file.GetGateway().GetModelRoles() {
		roles[name] = gateway.Role{Model: r.GetModel(), Fallback: r.GetFallback()}
	}
	return roles
}

// checkModelRoles refuses a workflow that names a model role which the
// configuration file does not map (decision 0233).
func checkModelRoles(cfg *config, wf *workflow.Workflow) error {
	roles := cfg.file.GetGateway().GetModelRoles()
	var missing []string
	for _, r := range wf.ModelRoles() {
		if _, ok := roles[r]; !ok {
			missing = append(missing, strconv.Quote(r))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the workflow names the model roles %s, which gateway.model_roles of the configuration file does not map", strings.Join(missing, ", "))
	}
	return nil
}

// describe says where the gateway serves and which credential reaches the
// provider.
func (m *modelGateway) describe(cfg *config) string {
	if m.KeepsSignIn() {
		return fmt.Sprintf("The model gateway serves at %s; the harness keeps its own sign-in.", m.URL())
	}
	return fmt.Sprintf("The model gateway serves at %s; it sends the API key of %s.", m.URL(), cfg.file.GetGateway().GetApiKeyEnv())
}
