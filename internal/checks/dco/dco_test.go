// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package dco

import (
	"reflect"
	"strings"
	"testing"
)

const (
	jane  = "Jane Doe <jane@example.com>"
	agent = "Claude <noreply@anthropic.com>"
)

var agents = []Identity{{Name: "Claude", Email: "noreply@anthropic.com", Reason: "test"}}

// commit returns a commit with one parent, the given author and the given
// message.
func commit(author, message string) Commit {
	name, email, _ := strings.Cut(strings.TrimSuffix(author, ">"), " <")
	return Commit{
		Hash:        "0123456789abcdef0123456789abcdef01234567",
		Parents:     []string{"1111111111111111111111111111111111111111"},
		AuthorName:  name,
		AuthorEmail: email,
		Message:     message,
	}
}

func TestCheck(t *testing.T) {
	merge := commit(jane, "Merge branch 'main' into topic\n")
	merge.Parents = append(merge.Parents, "2222222222222222222222222222222222222222")
	signedMerge := commit(jane, "Merge branch 'main' into topic\n\nSigned-off-by: "+jane+"\n")
	signedMerge.Parents = merge.Parents
	agentMerge := commit(agent, "Merge branch 'main' into topic\n\nSigned-off-by: "+agent+"\n")
	agentMerge.Parents = merge.Parents

	tests := []struct {
		name   string
		commit Commit
		// want is the problem reported, or "" when the commit passes.
		want string
	}{
		{
			name:   "matching sign-off",
			commit: commit(jane, "Add a feature\n\nSigned-off-by: "+jane+"\n"),
		},
		{
			name:   "matching sign-off among other trailers",
			commit: commit(jane, "Add a feature\n\nBody.\n\nAssisted-by: Tool (model)\nSigned-off-by: Bob <bob@example.com>\nSigned-off-by: "+jane+"\nCo-authored-by: Bob <bob@example.com>\n"),
		},
		{
			name:   "matching sign-off with the email in another case",
			commit: commit(jane, "Add a feature\n\nSigned-off-by: Jane Doe <Jane@Example.COM>\n"),
		},
		{
			name:   "matching sign-off with the key in another case and CRLF line endings",
			commit: commit(jane, "Add a feature\r\n\r\nsigned-off-by: "+jane+"\r\n"),
		},
		{
			name:   "missing sign-off",
			commit: commit(jane, "Add a feature\n"),
			want:   "no Signed-off-by line; add one that matches the author Jane Doe <jane@example.com>, such as with git rebase --signoff",
		},
		{
			name:   "mismatched sign-off name",
			commit: commit(jane, "Add a feature\n\nSigned-off-by: Jane <jane@example.com>\n"),
			want:   "no Signed-off-by line matches the author Jane Doe <jane@example.com>",
		},
		{
			name:   "mismatched sign-off email",
			commit: commit(jane, "Add a feature\n\nSigned-off-by: Jane Doe <jane@example.org>\n"),
			want:   "no Signed-off-by line matches the author Jane Doe <jane@example.com>",
		},
		{
			name:   "sign-off that is not a whole line",
			commit: commit(jane, "Add a feature\n\nThe text Signed-off-by: "+jane+" is quoted here.\n"),
			want:   "no Signed-off-by line; add one that matches the author Jane Doe <jane@example.com>, such as with git rebase --signoff",
		},
		{
			name:   "malformed sign-off",
			commit: commit(jane, "Add a feature\n\nSigned-off-by: Jane Doe jane@example.com\n"),
			want:   "no Signed-off-by line matches the author Jane Doe <jane@example.com>",
		},
		{
			name:   "listed agent with a person's sign-off",
			commit: commit(agent, "Add a feature\n\nAssisted-by: Claude Code (model)\nSigned-off-by: "+jane+"\n"),
		},
		{
			name:   "listed agent with only its own sign-off",
			commit: commit(agent, "Add a feature\n\nSigned-off-by: "+agent+"\n"),
			want:   "the author Claude <noreply@anthropic.com> is a listed agent identity, and no Signed-off-by line is from a person",
		},
		{
			name:   "listed agent with its own email under another name and case",
			commit: commit(agent, "Add a feature\n\nSigned-off-by: Claude Code <NoReply@Anthropic.com>\n"),
			want:   "the author Claude <noreply@anthropic.com> is a listed agent identity, and no Signed-off-by line is from a person",
		},
		{
			name:   "listed agent with no sign-off",
			commit: commit(agent, "Add a feature\n"),
			want:   "the author Claude <noreply@anthropic.com> is a listed agent identity, and no Signed-off-by line is from a person",
		},
		{
			name:   "listed agent with a malformed sign-off",
			commit: commit(agent, "Add a feature\n\nSigned-off-by: nobody\n"),
			want:   "the author Claude <noreply@anthropic.com> is a listed agent identity, and no Signed-off-by line is from a person",
		},
		{
			name:   "listed agent with an empty sign-off and one with text after the email",
			commit: commit(agent, "Add a feature\n\nSigned-off-by:\nSigned-off-by: Jane Doe <jane@example.com>, extra\n"),
			want:   "the author Claude <noreply@anthropic.com> is a listed agent identity, and no Signed-off-by line is from a person",
		},
		{
			name:   "agent email under another name with a person's sign-off",
			commit: commit("Claude Code <noreply@anthropic.com>", "Add a feature\n\nSigned-off-by: "+jane+"\n"),
		},
		{
			name:   "agent email under another name with only its own sign-off",
			commit: commit("claude <NoReply@anthropic.com>", "Add a feature\n\nSigned-off-by: claude <NoReply@anthropic.com>\n"),
			want:   "the author claude <NoReply@anthropic.com> is a listed agent identity, and no Signed-off-by line is from a person",
		},
		{
			name:   "signed merge commit",
			commit: signedMerge,
		},
		{
			name:   "agent merge commit with only its own sign-off",
			commit: agentMerge,
			want: "the author Claude <noreply@anthropic.com> is a listed agent identity, and no Signed-off-by line is from a person. " +
				`This is a merge commit. If GitHub's "Update branch" button made it, remove it and update the branch with "Update with rebase" instead, ` +
				"because GitHub's merge commit has no sign-off. An agent updates a branch with git merge --signoff",
		},
		{
			name:   "merge commit without a sign-off",
			commit: merge,
			want: "no Signed-off-by line; add one that matches the author Jane Doe <jane@example.com>, such as with git rebase --signoff. " +
				`This is a merge commit. If GitHub's "Update branch" button made it, remove it and update the branch with "Update with rebase" instead, ` +
				"because GitHub's merge commit has no sign-off. An agent updates a branch with git merge --signoff",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Check([]Commit{tt.commit}, agents)
			var want []Finding
			if tt.want != "" {
				want = []Finding{{Commit: tt.commit, Problem: tt.want}}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Check() = %#v,\nwant %#v", got, want)
			}
		})
	}
}

func TestCheckReportsEachFailingCommitInOrder(t *testing.T) {
	first := commit(jane, "First\n")
	first.Hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	good := commit(jane, "Good\n\nSigned-off-by: "+jane+"\n")
	last := commit(agent, "Last\n")
	last.Hash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	got := Check([]Commit{first, good, last}, agents)
	if len(got) != 2 || got[0].Commit.Hash != first.Hash || got[1].Commit.Hash != last.Hash {
		t.Errorf("Check() = %#v, want findings for the first and last commits", got)
	}
}

func TestFindingString(t *testing.T) {
	f := Finding{Commit: commit(jane, "Add a feature\n\nBody.\n"), Problem: "no Signed-off-by line"}
	want := `0123456789ab "Add a feature": no Signed-off-by line`
	if got := f.String(); got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestParseIdentities(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    []Identity
		wantErr string
	}{
		{
			name: "one identity",
			data: `{"identities": [{"name": "Claude", "email": "noreply@anthropic.com", "reason": "Claude Code"}]}`,
			want: []Identity{{Name: "Claude", Email: "noreply@anthropic.com", Reason: "Claude Code"}},
		},
		{
			name: "no identities",
			data: `{"identities": []}`,
			want: []Identity{},
		},
		{
			name:    "unknown field in an identity",
			data:    `{"identities": [{"name": "Claude", "email": "noreply@anthropic.com", "reason": "Claude Code", "comment": "x"}]}`,
			wantErr: `unknown field "comment"`,
		},
		{
			name:    "unknown top-level field",
			data:    `{"identities": [], "agents": []}`,
			wantErr: `unknown field "agents"`,
		},
		{
			name:    "missing identities",
			data:    `{}`,
			wantErr: `"identities" is missing`,
		},
		{
			name:    "missing reason",
			data:    `{"identities": [{"name": "Claude", "email": "noreply@anthropic.com"}]}`,
			wantErr: `identity 1: "reason" is empty`,
		},
		{
			name:    "missing name",
			data:    `{"identities": [{"email": "noreply@anthropic.com", "reason": "Claude Code"}]}`,
			wantErr: `identity 1: "name" is empty`,
		},
		{
			name:    "missing email",
			data:    `{"identities": [{"name": "Claude", "reason": "Claude Code"}]}`,
			wantErr: `identity 1: "email" is empty`,
		},
		{
			name:    "duplicate email",
			data:    `{"identities": [{"name": "A", "email": "a@example.com", "reason": "r"}, {"name": "B", "email": "A@example.com", "reason": "r"}]}`,
			wantErr: `identity 2: the email A@example.com is listed twice`,
		},
		{
			name:    "comment",
			data:    "{\"identities\": [] // none\n}",
			wantErr: "invalid character '/'",
		},
		{
			name:    "trailing data",
			data:    `{"identities": []} {}`,
			wantErr: "data after the JSON object",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIdentities([]byte(tt.data))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseIdentities() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseIdentities() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseIdentities() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseLog(t *testing.T) {
	out := "aaaa\x1fbbbb cccc\x1fJane Doe\x1fjane@example.com\x1fMerge\n\nSigned-off-by: Jane Doe <jane@example.com>\n\x00" +
		"dddd\x1f\x1fBob\x1fbob@example.com\x1fRoot\n\x00"
	want := []Commit{
		{Hash: "aaaa", Parents: []string{"bbbb", "cccc"}, AuthorName: "Jane Doe", AuthorEmail: "jane@example.com", Message: "Merge\n\nSigned-off-by: Jane Doe <jane@example.com>\n"},
		{Hash: "dddd", Parents: []string{}, AuthorName: "Bob", AuthorEmail: "bob@example.com", Message: "Root\n"},
	}
	got, err := ParseLog([]byte(out))
	if err != nil {
		t.Fatalf("ParseLog() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLog() = %#v,\nwant %#v", got, want)
	}
}

func TestParseLogRejectsARecordWithTooFewFields(t *testing.T) {
	_, err := ParseLog([]byte("aaaa\x1fbbbb\x1fJane\x00"))
	if err == nil || !strings.Contains(err.Error(), "record 1 has 3 fields, want 5") {
		t.Errorf("ParseLog() error = %v, want one about the fields of record 1", err)
	}
}

func TestParseLogOfNoOutput(t *testing.T) {
	got, err := ParseLog(nil)
	if err != nil || len(got) != 0 {
		t.Errorf("ParseLog(nil) = %v, %v; want no commits and no error", got, err)
	}
}
