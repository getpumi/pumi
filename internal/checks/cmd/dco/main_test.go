// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const identities = `{"identities": [{"name": "Claude", "email": "noreply@anthropic.com", "reason": "test"}]}`

// git runs cmd in the working directory as the author Jane Doe, or as Claude
// when asAgent is true, with message as its standard input. The arguments of
// each command are constants, because gosec reports a command that is built
// from variables (G204).
func git(t *testing.T, asAgent bool, message string, cmd *exec.Cmd) {
	t.Helper()
	name, email := "Jane Doe", "jane@example.com"
	if asAgent {
		name, email = "Claude", "noreply@anthropic.com"
	}
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME="+name, "GIT_COMMITTER_EMAIL="+email)
	cmd.Stdin = strings.NewReader(message)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
}

// newRepo makes a repository in a new working directory with a base commit on
// main and a branch topic that has the given commits, and writes identities
// to .pumi/agent-identities.json unless it is empty. Each commit is made by
// Claude when its message starts with "agent:".
func newRepo(t *testing.T, identities string, messages ...string) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git(t, false, "", exec.CommandContext(t.Context(), "git", "init", "--quiet", "--initial-branch=main"))
	git(t, false, "Base\n", exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--allow-empty", "--file=-"))
	git(t, false, "", exec.CommandContext(t.Context(), "git", "checkout", "--quiet", "-b", "topic"))
	for _, m := range messages {
		git(t, strings.HasPrefix(m, "agent:"), m, exec.CommandContext(t.Context(), "git", "commit", "--quiet", "--allow-empty", "--file=-"))
	}
	if identities == "" {
		return
	}
	if err := os.Mkdir(".pumi", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(".pumi", "agent-identities.json"), []byte(identities), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunPassesWhenEveryCommitIsSignedOff(t *testing.T) {
	newRepo(t, identities,
		"One\n\nSigned-off-by: Jane Doe <jane@example.com>\n",
		"agent: Two\n\nSigned-off-by: Jane Doe <jane@example.com>\n")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"main..topic"}, &stdout, &stderr); code != 0 {
		t.Errorf("run() = %d, want 0; stderr: %s", code, stderr.String())
	}
	if got, want := stdout.String(), "dco: commits checked: 2, all signed off\n"; got != want {
		t.Errorf("run() wrote stdout %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("run() wrote stderr %q, want nothing", stderr.String())
	}
}

func TestRunFailsOnCommitsWithoutSignOff(t *testing.T) {
	newRepo(t, identities,
		"One\n\nSigned-off-by: Jane Doe <jane@example.com>\n",
		"Two\n",
		"agent: Three\n\nSigned-off-by: Claude <noreply@anthropic.com>\n")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"main..topic"}, &stdout, &stderr); code != 1 {
		t.Errorf("run() = %d, want 1", code)
	}
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 2 ||
		!strings.Contains(lines[0], ` "Two": no Signed-off-by line; `) ||
		!strings.Contains(lines[1], ` "agent: Three": the author Claude <noreply@anthropic.com> is a listed agent identity`) {
		t.Errorf("run() wrote stdout %q, want a finding for Two and then Three, oldest first", stdout.String())
	}
	if got, want := stderr.String(), "dco: commits that fail the DCO check (decisions 0070 and 0081): 2 of 3\n"; got != want {
		t.Errorf("run() wrote stderr %q, want %q", got, want)
	}
}

func TestRunReportsErrors(t *testing.T) {
	tests := []struct {
		name       string
		identities string
		args       []string
		want       string
	}{
		{"no range", identities, nil, "usage: dco <base>..<head>\n"},
		{"two ranges", identities, []string{"main..topic", "main..topic"}, "usage: dco <base>..<head>\n"},
		{"no commits", identities, []string{"topic..main"}, `dco: the range "topic..main" has no commits` + "\n"},
		{"bad range", identities, []string{"nonsense..topic"}, "dco: git log: "},
		{"no identities file", "", []string{"main..topic"}, "dco: open .pumi/agent-identities.json: "},
		{"bad identities file", `{"identities": [], "x": 1}`, []string{"main..topic"}, `dco: .pumi/agent-identities.json: json: unknown field "x"` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newRepo(t, tt.identities, "One\n\nSigned-off-by: Jane Doe <jane@example.com>\n")
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != 2 {
				t.Errorf("run() = %d, want 2", code)
			}
			if !strings.HasPrefix(stderr.String(), tt.want) {
				t.Errorf("run() wrote stderr %q, want it to start with %q", stderr.String(), tt.want)
			}
		})
	}
}
