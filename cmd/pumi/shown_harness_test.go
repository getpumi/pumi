// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
)

// writeAdapter writes a file that stands for an adapter and returns its
// absolute path.
func writeAdapter(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "adapter", "adapter")
	return filepath.Join(dir, "adapter")
}

func TestTheShownHarnessIsClaudeCodeByDefault(t *testing.T) {
	t.Setenv(envHarness, "")
	h, err := shownHarnessOf()
	if err != nil {
		t.Fatal(err)
	}
	if h.name != "claude-code" || h.binary != "adapter-claude-code" || h.process != "claude" {
		t.Errorf("shownHarnessOf() = %+v, want Claude Code", h)
	}
}

func TestTheShownHarnessRefusesAHarnessItDoesNotKnow(t *testing.T) {
	t.Setenv(envHarness, "gemini")
	if h, err := shownHarnessOf(); err == nil {
		t.Errorf("shownHarnessOf() = %+v, want an error", h)
	}
}

// TestTheShownCodexRunsUseTheCodexAdapterAndOpenAIModels: with
// PUMI_SHOWN_HARNESS=codex, the model configuration of the shown runs names
// the Codex adapter for the role, its fallback and the checks, with
// OpenAI's models, in the API key mode when PUMI_SHOWN_API_KEY_ENV names
// the key's variable (decisions 0243 and 0245).
func TestTheShownCodexRunsUseTheCodexAdapterAndOpenAIModels(t *testing.T) {
	t.Setenv(envHarness, "codex")
	t.Setenv(envProviderVariable, "PUMI_OPENAI_KEY")
	path := writeAdapter(t)
	m, err := shownModels(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	a := m.GetAdapters()["codex"]
	if len(m.GetAdapters()) != 1 || a.GetExecutable() != path || a.GetSha256() == "" {
		t.Fatalf("the adapters are %v, want codex at %s with its hash", m.GetAdapters(), path)
	}
	if a.GetCredentialMode() != configv1.Adapter_CREDENTIAL_MODE_API_KEY || a.GetApiKeyEnv() != "PUMI_OPENAI_KEY" {
		t.Errorf("codex is in %v with the key in %q, want the API key mode with PUMI_OPENAI_KEY", a.GetCredentialMode(), a.GetApiKeyEnv())
	}
	r := m.GetRoles()[shownRole]
	if r.GetAdapter() != "codex" || r.GetModel() != "gpt-6.2-sol" ||
		r.GetFallback().GetAdapter() != "codex" || r.GetFallback().GetModel() != "gpt-6.1-sol" {
		t.Errorf("the role %s is %v, want codex with gpt-6.2-sol and the fallback gpt-6.1-sol", shownRole, r)
	}
	if m.GetCheckAdapter() != "codex" {
		t.Errorf("the check adapter is %q, want codex", m.GetCheckAdapter())
	}
}

func TestTheShownCodexRunsOnASignInHaveNoKey(t *testing.T) {
	t.Setenv(envHarness, "codex")
	t.Setenv(envProviderVariable, "")
	m, err := shownModels(t.Context(), writeAdapter(t))
	if err != nil {
		t.Fatal(err)
	}
	a := m.GetAdapters()["codex"]
	if a.GetCredentialMode() != configv1.Adapter_CREDENTIAL_MODE_SIGN_IN || a.GetApiKeyEnv() != "" {
		t.Errorf("codex is in %v with the key in %q, want the sign-in mode with no key", a.GetCredentialMode(), a.GetApiKeyEnv())
	}
}

// TestTheGatewayTokenIsFoundInTheHarnessEnvironment: the shown runs find
// the invocation token where each adapter gives it to its harness: claude
// sends it as its bearer token on the API key path, and codex always in
// the header X-Pumi-Token, from the adapter's variable.
func TestTheGatewayTokenIsFoundInTheHarnessEnvironment(t *testing.T) {
	for name, c := range map[string]struct {
		harness, environ string
		want             gatewayToken
	}{
		"claude bearer": {"claude-code", "HOME=/h\x00ANTHROPIC_AUTH_TOKEN=tok-1\x00", gatewayToken{value: "tok-1"}},
		"claude header": {"claude-code", "ANTHROPIC_CUSTOM_HEADERS=X-Pumi-Token: tok-2\x00", gatewayToken{value: "tok-2", header: true}},
		"codex header":  {"codex", "PATH=/bin\x00PUMI_CODEX_GATEWAY_HEADER=tok-3\x00", gatewayToken{value: "tok-3", header: true}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(envHarness, c.harness)
			h, err := shownHarnessOf()
			if err != nil {
				t.Fatal(err)
			}
			got, ok := h.tokenIn([]byte(c.environ))
			if !ok || got != c.want {
				t.Errorf("tokenIn = %+v, %v, want %+v", got, ok, c.want)
			}
		})
	}
	t.Setenv(envHarness, "codex")
	h, err := shownHarnessOf()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := h.tokenIn([]byte("ANTHROPIC_AUTH_TOKEN=tok-1\x00")); ok {
		t.Errorf("tokenIn of codex found %+v in claude's variable", got)
	}
}

// TestOnlyTheAPIKeyModeHasAKeyPathToCheck: the Codex adapter gives codex
// the token in a header while the gateway holds the key, and Claude Code's
// adapter gives claude the token as its bearer token.
func TestOnlyTheAPIKeyModeHasAKeyPathToCheck(t *testing.T) {
	for harness, want := range map[string]bool{"claude-code": false, "codex": true} {
		t.Setenv(envHarness, harness)
		h, err := shownHarnessOf()
		if err != nil {
			t.Fatal(err)
		}
		if h.keyPathHeader != want {
			t.Errorf("%s: keyPathHeader = %v, want %v", harness, h.keyPathHeader, want)
		}
	}
}

// TestTheShownRunsLetCommandsReadGo: the adapter of the shown runs lists
// Go's root and module cache as read paths, so that a harness that runs
// commands in a sandbox can run the go test commands of the plan with the
// toolchain and modules of the machine (decision 0269).
func TestTheShownRunsLetCommandsReadGo(t *testing.T) {
	m, err := shownModels(t.Context(), writeAdapter(t))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Fields(string(out))
	for _, a := range m.GetAdapters() {
		if !slices.Equal(a.GetReadPaths(), want) {
			t.Errorf("the read paths are %q, want Go's root and module cache %q", a.GetReadPaths(), want)
		}
	}
}
