// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"slices"
	"strings"
	"testing"
)

// validSHA is a SHA-256 in the form that the model configuration takes.
var validSHA = strings.Repeat("0a", 32)

// writeTestConfig writes a configuration file with models, text format, as
// its model configuration, and returns its path.
func writeTestConfig(t *testing.T, models string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "pumi.txtpb", `workflow: "workflow.txtpb"
trunk: "main"
project { name: "case" }
`+models+"\n")
	return dir + "/pumi.txtpb"
}

// adapterText returns an adapter entry named name, in text format, with
// the fields fields.
func adapterText(name, fields string) string {
	return `adapters { key: "` + name + `" value { ` + fields + ` } }`
}

// The parts of the model configurations of TestReadConfig.
var (
	signedIn    = adapterText("claude-code", `executable: "bin/adapter-claude-code" sha256: "`+validSHA+`" credential_mode: CREDENTIAL_MODE_SIGN_IN`)
	keyed       = adapterText("codex", `executable: "/opt/adapter-codex" sha256: "`+validSHA+`" credential_mode: CREDENTIAL_MODE_API_KEY api_key_env: "OPENAI_API_KEY" read_paths: "/home/someone/go"`)
	implementer = `roles { key: "implementer" value { adapter: "claude-code" model: "claude-opus-5-5" fallback { adapter: "codex" model: "astra" } } }`
	checks      = `check_adapter: "claude-code"`
)

// models returns a models block of parts.
func models(parts ...string) string {
	return "models { " + strings.Join(parts, " ") + " }"
}

// TestAFallbackOnTheSameAdapterIsTheGatewaysFallbackModel: a role whose
// fallback is on its own adapter gives the gateway its fallback model
// (decision 0233).
func TestAFallbackOnTheSameAdapterIsTheGatewaysFallbackModel(t *testing.T) {
	same := `roles { key: "implementer" value { adapter: "claude-code" model: "claude-opus-5-5" fallback { adapter: "claude-code" model: "claude-sonnet-5-5" } } }`
	cfg, err := readConfig(writeTestConfig(t, models(signedIn, same, checks)))
	if err != nil {
		t.Fatal(err)
	}
	if got := modelRoles(cfg)["implementer"]; got.Model != "claude-opus-5-5" || got.Fallback != "claude-sonnet-5-5" {
		t.Errorf("the gateway's role implementer is %+v, want claude-opus-5-5 falling back to claude-sonnet-5-5", got)
	}
}

// TestReadConfig: the configuration file has a model configuration apart
// from the profile settings, with adapters by name and a role on each, and
// a file that breaks its rules is refused when it is read, with an error
// that says why (decisions 0240, 0245 and 0253).
func TestReadConfig(t *testing.T) {
	cfg, err := readConfig(writeTestConfig(t, models(signedIn, keyed, implementer, checks)))
	if err != nil {
		t.Fatalf("readConfig = %v, want the file read", err)
	}
	a := cfg.adapters()
	if got := a["claude-code"]; got.Path != cfg.dir+"/bin/adapter-claude-code" || got.SHA256 != validSHA || !got.SignIn {
		t.Errorf("the adapter claude-code is %+v, want the path in the file's directory, its SHA-256 and the sign-in mode", got)
	}
	if got := a["codex"]; got.Path != "/opt/adapter-codex" || got.SignIn || !slices.Equal(got.ReadPaths, []string{"/home/someone/go"}) {
		t.Errorf("the adapter codex is %+v, want its absolute path, the API key mode and its read path", got)
	}
	if got := cfg.roleAdapters(); len(got) != 1 || got["implementer"] != "claude-code" {
		t.Errorf("the roles' adapters are %v, want implementer on claude-code", got)
	}
	// The fallback is on another adapter, so the gateway has no fallback
	// model for the role (decision 0246).
	if got := modelRoles(cfg); len(got) != 1 || got["implementer"].Model != "claude-opus-5-5" || got["implementer"].Fallback != "" {
		t.Errorf("the gateway's roles are %+v, want implementer on claude-opus-5-5 with no fallback model", got)
	}

	for name, tc := range map[string]struct{ file, want string }{
		"no models":   {"", "models"},
		"no adapters": {models(implementer, checks), "models.adapters"},
		"an unknown adapter": {
			models(signedIn, `roles { key: "implementer" value { adapter: "gemini" model: "m" } }`, checks), `"gemini"`,
		},
		"a role without an adapter": {
			models(signedIn, `roles { key: "implementer" value { model: "claude-opus-5-5" } }`, checks), `"implementer"`,
		},
		"a role without a model": {
			models(signedIn, `roles { key: "implementer" value { adapter: "claude-code" } }`, checks), `"implementer"`,
		},
		"a fallback on an unknown adapter": {
			models(signedIn, `roles { key: "implementer" value { adapter: "claude-code" model: "a" fallback { adapter: "gemini" model: "b" } } }`, checks), `"gemini"`,
		},
		"a fallback without a model": {
			models(signedIn, `roles { key: "implementer" value { adapter: "claude-code" model: "a" fallback { adapter: "claude-code" } } }`, checks), "fallback",
		},
		"a fallback that is the role itself": {
			models(signedIn, `roles { key: "implementer" value { adapter: "claude-code" model: "a" fallback { adapter: "claude-code" model: "a" } } }`, checks), "fallback",
		},
		"a default role without an adapter": {
			models(signedIn, `default_role { model: "claude-opus-5-5" }`, checks), "default_role",
		},
		"a default role on an unknown adapter": {
			models(signedIn, `default_role { adapter: "gemini" model: "m" }`, checks), `"gemini"`,
		},
		"a default role whose fallback is itself": {
			models(signedIn, `default_role { adapter: "claude-code" model: "a" fallback { adapter: "claude-code" model: "a" } }`, checks), "default_role",
		},
		"a sign-in entry with a key variable": {
			models(adapterText("claude-code", `executable: "a" sha256: "`+validSHA+`" credential_mode: CREDENTIAL_MODE_SIGN_IN api_key_env: "ANTHROPIC_API_KEY"`), checks),
			"ANTHROPIC_API_KEY",
		},
		"an API key entry without a key variable": {
			models(adapterText("claude-code", `executable: "a" sha256: "`+validSHA+`" credential_mode: CREDENTIAL_MODE_API_KEY`), checks), "api_key_env",
		},
		"an entry without a credential mode": {
			models(adapterText("claude-code", `executable: "a" sha256: "`+validSHA+`"`), checks), "credential_mode",
		},
		"a read path that is not absolute": {
			models(adapterText("claude-code", `executable: "a" sha256: "`+validSHA+`" credential_mode: CREDENTIAL_MODE_SIGN_IN read_paths: "go/pkg"`), checks),
			`read path "go/pkg"`,
		},
		"an entry without an executable": {
			models(adapterText("claude-code", `sha256: "`+validSHA+`" credential_mode: CREDENTIAL_MODE_SIGN_IN`), checks), "executable",
		},
		"an entry without a SHA-256": {
			models(adapterText("claude-code", `executable: "a" credential_mode: CREDENTIAL_MODE_SIGN_IN`), checks), "sha256",
		},
		"an entry with an uppercase SHA-256": {
			models(adapterText("claude-code", `executable: "a" sha256: "`+strings.ToUpper(validSHA)+`" credential_mode: CREDENTIAL_MODE_SIGN_IN`), checks), "sha256",
		},
		"no check adapter":         {models(signedIn, implementer, keyed), "check_adapter"},
		"an unknown check adapter": {models(signedIn, `check_adapter: "gemini"`), `"gemini"`},
		"the replaced adapter field": {
			`adapter: "bin/adapter" ` + models(signedIn, checks), "models",
		},
		"the replaced gateway section": {
			`gateway { api_key_env: "ANTHROPIC_API_KEY" } ` + models(signedIn, checks), "models",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readConfig(writeTestConfig(t, tc.file))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("readConfig = %v, want an error that names %s", err, tc.want)
			}
		})
	}
}
