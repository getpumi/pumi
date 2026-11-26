// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// workspaceFiles are the files of every scenario's workspace, by their
// paths relative to it, besides secretFile. The directories hold a file so
// that git keeps them.
var workspaceFiles = map[string]string{
	"README.md":     "This is the workspace of a Pumi conformance run.\n",
	"src/.keep":     "",
	"lib/src/.keep": "",
	"docs/.keep":    "",
	"out/.keep":     "",
}

// secretFile is the file that holds the world's secret, which a run that
// may not read files must not pass to the model.
const secretFile = "secret.txt"

// makeWorkspace writes the workspace files to dir, with secret in
// secretFile, and makes dir a git repository with one commit of them, as
// the runner does (decision 0247). It returns the commit's tree.
func makeWorkspace(ctx context.Context, dir, secret string) (string, error) {
	files := map[string]string{secretFile: secret + "\n"}
	for name, content := range workspaceFiles {
		files[name] = content
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return "", err
		}
	}
	// Every git argument is a constant; the directory varies.
	for _, cmd := range []*exec.Cmd{
		exec.CommandContext(ctx, "git", "init", "--quiet", "--template="),
		exec.CommandContext(ctx, "git", "add", "--all"),
		exec.CommandContext(ctx, "git", "commit", "--quiet", "--no-verify", "--message", "The input of a Pumi conformance run"),
	} {
		if _, err := git(cmd, dir); err != nil {
			return "", err
		}
	}
	tree, err := git(exec.CommandContext(ctx, "git", "rev-parse", "HEAD^{tree}"), dir)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tree), nil
}

// git runs the git command cmd in dir with the person's own configuration
// left out, and returns its standard output.
func git(cmd *exec.Cmd, dir string) (string, error) {
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Pumi conformance", "GIT_AUTHOR_EMAIL=conformance@getpumi.dev",
		"GIT_COMMITTER_NAME=Pumi conformance", "GIT_COMMITTER_EMAIL=conformance@getpumi.dev",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
