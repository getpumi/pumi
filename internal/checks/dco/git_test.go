// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package dco

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// repo is a git repository in a temporary directory. Its commands have
// constant arguments, because gosec reports a command that is built from
// variables (G204); the author and the message vary through the environment
// and standard input.
type repo struct {
	t   *testing.T
	dir string
}

// newRepo creates an empty repository whose branch is main. It turns off the
// user's and the system's git configuration for the test, so that a setting
// such as commit.gpgSign does not change the commits.
func newRepo(t *testing.T) *repo {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	r := &repo{t: t, dir: t.TempDir()}
	r.run("", "", exec.CommandContext(r.t.Context(), "git", "init", "--quiet", "--initial-branch=main"))
	return r
}

// run runs cmd in the repository as author, a "Name <email>" string, with
// stdin as its standard input, and returns its standard output.
func (r *repo) run(author, stdin string, cmd *exec.Cmd) string {
	r.t.Helper()
	name, email, _ := strings.Cut(strings.TrimSuffix(author, ">"), " <")
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME=Committer", "GIT_COMMITTER_EMAIL=committer@example.com")
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("%s: %v\n%s", cmd, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// commit adds an empty commit to the current branch and returns its hash.
func (r *repo) commit(author, message string) string {
	r.t.Helper()
	r.run(author, message, exec.CommandContext(r.t.Context(), "git", "commit", "--quiet", "--allow-empty", "--cleanup=verbatim", "--file=-"))
	return r.run("", "", exec.CommandContext(r.t.Context(), "git", "rev-parse", "HEAD"))
}

func TestReadCommits(t *testing.T) {
	r := newRepo(t)
	base := r.commit(jane, "Base\n")
	r.run("", "", exec.CommandContext(r.t.Context(), "git", "checkout", "--quiet", "-b", "side"))
	side := r.commit(jane, "Side\n\nSigned-off-by: "+jane+"\n")
	r.run("", "", exec.CommandContext(r.t.Context(), "git", "checkout", "--quiet", "main"))
	r.commit(jane, "Main\n")
	r.run("", "", exec.CommandContext(r.t.Context(), "git", "checkout", "--quiet", "-b", "topic", "main~1"))
	first := r.commit(agent, "First\n\nSigned-off-by: "+jane+"\n")
	r.run("", "", exec.CommandContext(t.Context(), "git", "merge", "--quiet", "--no-ff", "--no-commit", "side"))
	merge := r.commit(jane, "Merge branch 'side' into topic\n")

	got, err := ReadCommits(t.Context(), r.dir, "main..topic")
	if err != nil {
		t.Fatalf("ReadCommits() error = %v", err)
	}
	want := []Commit{
		{Hash: merge, Parents: []string{first, side}, AuthorName: "Jane Doe", AuthorEmail: "jane@example.com", Message: "Merge branch 'side' into topic\n"},
		{Hash: side, Parents: []string{base}, AuthorName: "Jane Doe", AuthorEmail: "jane@example.com", Message: "Side\n\nSigned-off-by: " + jane + "\n"},
		{Hash: first, Parents: []string{base}, AuthorName: "Claude", AuthorEmail: "noreply@anthropic.com", Message: "First\n\nSigned-off-by: " + jane + "\n"},
	}
	if len(got) != len(want) {
		t.Fatalf("ReadCommits() returned %d commits, want %d: %#v", len(got), len(want), got)
	}
	// git log lists the commits newest first, and two commits made in the
	// same second can come in either order, so the test compares them by
	// hash.
	byHash := map[string]Commit{}
	for _, c := range got {
		byHash[c.Hash] = c
	}
	for _, w := range want {
		if c, ok := byHash[w.Hash]; !ok || !commitsEqual(c, w) {
			t.Errorf("ReadCommits() has %#v,\nwant %#v", c, w)
		}
	}
}

func commitsEqual(a, b Commit) bool {
	return a.Hash == b.Hash && strings.Join(a.Parents, " ") == strings.Join(b.Parents, " ") &&
		a.AuthorName == b.AuthorName && a.AuthorEmail == b.AuthorEmail && a.Message == b.Message
}

func TestReadCommitsOfAnEmptyRange(t *testing.T) {
	r := newRepo(t)
	r.commit(jane, "Base\n")
	got, err := ReadCommits(t.Context(), r.dir, "main..main")
	if err != nil || len(got) != 0 {
		t.Errorf("ReadCommits() = %v, %v; want no commits and no error", got, err)
	}
}

func TestReadCommitsRejectsBadRanges(t *testing.T) {
	r := newRepo(t)
	r.commit(jane, "Base\n")
	tests := []struct {
		name, revRange, wantErr string
	}{
		{"unknown revision", "nonsense..main", "bad revision 'nonsense..main'"},
		{"option", "--all", `the range "--all" starts with "-"`},
		{"two lines", "main\n^main", `the range "main\n^main" has more than one line`},
		{"empty", "", "the range is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadCommits(t.Context(), r.dir, tt.revRange)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ReadCommits() error = %v, want one containing %q", err, tt.wantErr)
			}
		})
	}
}
