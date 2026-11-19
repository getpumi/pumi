// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package dco is the Developer Certificate of Origin check (decision 0070). It
// checks each commit of a pull request, merge commits included, for a
// Signed-off-by line whose name and email match the commit author. When the
// author is one of the agent identities listed in .pumi/agent-identities.json
// (decisions 0081, 0083 and 0112), the commit needs a Signed-off-by line from a
// person instead.
package dco

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Identity is one agent identity from .pumi/agent-identities.json. Adding one
// loosens the gate and is reviewed as such (decision 0083).
type Identity struct {
	Name   string `json:"name"`
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

// ParseIdentities reads the agent identities file. It rejects unknown fields,
// so a misspelled field is not silently ignored, and it requires every field
// of every identity, because JSON has no comments and the reason for each
// identity is a field (decision 0112).
func ParseIdentities(data []byte) ([]Identity, error) {
	var file struct {
		Identities *[]Identity `json:"identities"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("data after the JSON object")
	}
	if file.Identities == nil {
		return nil, errors.New(`"identities" is missing`)
	}
	ids := *file.Identities
	seen := map[string]bool{}
	for i, id := range ids {
		for _, f := range []struct{ name, value string }{{"name", id.Name}, {"email", id.Email}, {"reason", id.Reason}} {
			if strings.TrimSpace(f.value) == "" {
				return nil, fmt.Errorf("identity %d: %q is empty", i+1, f.name)
			}
		}
		email := strings.ToLower(id.Email)
		if seen[email] {
			return nil, fmt.Errorf("identity %d: the email %s is listed twice", i+1, id.Email)
		}
		seen[email] = true
	}
	return ids, nil
}

// Commit is one commit as git log reports it. Parents has two or more hashes
// for a merge commit and none for a root commit.
type Commit struct {
	Hash        string
	Parents     []string
	AuthorName  string
	AuthorEmail string
	Message     string
}

// subject returns the first line of the commit message.
func (c Commit) subject() string {
	s, _, _ := strings.Cut(c.Message, "\n")
	return strings.TrimSuffix(s, "\r")
}

// Finding is a commit that fails the check, with the reason.
type Finding struct {
	Commit  Commit
	Problem string
}

// String formats the finding as the short hash, the quoted subject and the
// problem.
func (f Finding) String() string {
	hash := f.Commit.Hash
	if len(hash) > 12 {
		hash = hash[:12]
	}
	return fmt.Sprintf("%s %q: %s", hash, f.Commit.subject(), f.Problem)
}

// mergeHint is added to the problem of a failing merge commit. GitHub's
// "Update branch" button makes a merge commit without a sign-off, and merge
// commits are checked like any other (getpumi/pumi#19).
const mergeHint = `. This is a merge commit. If GitHub's "Update branch" button made it, ` +
	`remove it and update the branch with "Update with rebase" instead, because GitHub's merge commit has no sign-off. ` +
	"An agent updates a branch with git merge --signoff"

// Check returns a finding for each commit that fails the check, in the order
// of commits. agents is the list of agent identities.
//
// An identity is recognized by its email alone, compared without regard to
// case, so that an agent cannot change its name to pass as a person: a commit's
// author is a listed agent when its email is the email of a listed identity,
// and a Signed-off-by line is from a person when it has the form
// "Name <email>" and its email is not the email of any listed identity. The
// name in the file is for the reader. For a commit by anyone else, the name
// and the email of a Signed-off-by line must both match the author; names must
// match exactly.
func Check(commits []Commit, agents []Identity) []Finding {
	var findings []Finding
	for _, c := range commits {
		problem := check(c, agents)
		if problem == "" {
			continue
		}
		if len(c.Parents) > 1 {
			problem += mergeHint
		}
		findings = append(findings, Finding{Commit: c, Problem: problem})
	}
	return findings
}

// check returns the problem with one commit, or "" when it passes.
func check(c Commit, agents []Identity) string {
	signoffs := signoffs(c.Message)
	author := fmt.Sprintf("%s <%s>", c.AuthorName, c.AuthorEmail)
	if isAgentEmail(c.AuthorEmail, agents) {
		for _, s := range signoffs {
			if s.email != "" && !isAgentEmail(s.email, agents) {
				return ""
			}
		}
		return "the author " + author + " is a listed agent identity, and no Signed-off-by line is from a person"
	}
	if len(signoffs) == 0 {
		return "no Signed-off-by line; add one that matches the author " + author + ", such as with git rebase --signoff"
	}
	for _, s := range signoffs {
		if s.name == c.AuthorName && strings.EqualFold(s.email, c.AuthorEmail) {
			return ""
		}
	}
	return "no Signed-off-by line matches the author " + author
}

func isAgentEmail(email string, agents []Identity) bool {
	for _, a := range agents {
		if strings.EqualFold(a.Email, email) {
			return true
		}
	}
	return false
}

// signoff is the identity in one Signed-off-by line. name and email are empty
// when the line does not have the form "Name <email>".
type signoff struct {
	name, email string
}

// signoffs returns the Signed-off-by lines of a commit message, in order. A
// line counts when it starts with the key "Signed-off-by:", in any case, as git
// compares trailer keys without regard to case. The lines may be anywhere in
// the message, as git commit --signoff puts them in the last paragraph but a
// person may add one by hand.
func signoffs(message string) []signoff {
	const key = "signed-off-by:"
	var out []signoff
	for line := range strings.Lines(message) {
		line = strings.TrimRight(line, "\r\n")
		if len(line) < len(key) || !strings.EqualFold(line[:len(key)], key) {
			continue
		}
		out = append(out, parseIdentity(strings.TrimSpace(line[len(key):])))
	}
	return out
}

// parseIdentity parses "Name <email>". It returns an empty signoff when value
// has another form.
func parseIdentity(value string) signoff {
	name, rest, ok := strings.Cut(value, "<")
	if !ok || !strings.HasSuffix(rest, ">") {
		return signoff{}
	}
	email := strings.TrimSuffix(rest, ">")
	name = strings.TrimSpace(name)
	if name == "" || email == "" || strings.ContainsAny(email, "<>") {
		return signoff{}
	}
	return signoff{name: name, email: email}
}

// logFormat makes git log print each commit as five fields separated by the
// unit separator: the hash, the parent hashes, the author name, the author
// email and the raw message. With -z, git log ends each commit with a NUL byte.
// %an and %ae are the author as recorded, not as a .mailmap file changes it.
const logFormat = "--format=%H%x1f%P%x1f%an%x1f%ae%x1f%B"

// ParseLog parses the output of git log with logFormat and -z.
func ParseLog(out []byte) ([]Commit, error) {
	var commits []Commit
	records := strings.Split(string(out), "\x00")
	for i, r := range records {
		if r == "" && i == len(records)-1 {
			break
		}
		f := strings.SplitN(r, "\x1f", 5)
		if len(f) != 5 {
			return nil, fmt.Errorf("git log: record %d has %d fields, want 5", i+1, len(f))
		}
		commits = append(commits, Commit{
			Hash:        f[0],
			Parents:     strings.Fields(f[1]),
			AuthorName:  f[2],
			AuthorEmail: f[3],
			Message:     f[4],
		})
	}
	return commits, nil
}

// ReadCommits runs git log in dir and returns the commits in revRange, such as
// "<base>..<head>", oldest first. Merge commits are included, and so are the
// commits that a merge brings in when they are not in base.
//
// The range goes to git log on standard input, so the command has constant
// arguments and a range cannot add an option to it. log.showSignature is
// turned off because it adds lines to the output.
func ReadCommits(ctx context.Context, dir, revRange string) ([]Commit, error) {
	switch {
	case revRange == "":
		return nil, errors.New("the range is empty")
	case strings.ContainsAny(revRange, "\r\n"):
		return nil, fmt.Errorf("the range %q has more than one line", revRange)
	case strings.HasPrefix(revRange, "-"):
		return nil, fmt.Errorf("the range %q starts with %q", revRange, "-")
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "log.showSignature=false",
		"log", "--stdin", "-z", "--reverse", logFormat)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(revRange + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git log: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return ParseLog(stdout.Bytes())
}
