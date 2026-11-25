// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// repository is a git repository that holds the trees of the invocations,
// such as the user's clone in embedded mode. The runner checks a tree out
// into a workspace with an index of its own, and computes the resulting
// tree from the workspace the same way, so the repository's own working
// tree, index and refs are never changed. Only new objects are written to
// it.
//
// Every git command has constant arguments: what varies is given in the
// environment (GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE), on standard
// input or as the working directory.
type repository struct {
	// gitDir is the absolute path of the git directory.
	gitDir string
	// hashLength is the length in hexadecimal digits of an object name of
	// the repository's object format.
	hashLength int
	// objectFormat is the repository's object format, sha1 or sha256.
	objectFormat string
	// emptyTree is the name of the empty tree.
	emptyTree string
}

// openRepository opens the repository that the directory dir is in.
func openRepository(ctx context.Context, dir string) (repository, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--absolute-git-dir", "--show-object-format")
	cmd.Dir = dir
	out, err := output(cmd, nil)
	if err != nil {
		return repository{}, fmt.Errorf("the directory %s is not in a git repository: %w", dir, err)
	}
	lines := strings.Fields(out)
	if len(lines) != 2 {
		return repository{}, fmt.Errorf("git rev-parse in %s printed %q", dir, out)
	}
	r := repository{gitDir: lines[0], objectFormat: lines[1]}
	switch lines[1] {
	case "sha1":
		r.hashLength = 40
	case "sha256":
		r.hashLength = 64
	default:
		return repository{}, fmt.Errorf("the repository %s has the object format %q", r.gitDir, lines[1])
	}
	// git mktree with no entries writes the empty tree and prints its name.
	empty, err := r.run(exec.CommandContext(ctx, "git", "mktree"), "", "", nil)
	if err != nil {
		return repository{}, err
	}
	r.emptyTree = strings.TrimSpace(empty)
	return r, nil
}

// run runs the git command cmd on the repository, with the work tree work
// and the index file index when they are not empty, the working directory
// work or else the git directory, and stdin on standard input. It returns
// the output without its last newline.
func (r repository) run(cmd *exec.Cmd, work, index string, stdin []byte) (string, error) {
	cmd.Env = append(os.Environ(), "GIT_DIR="+r.gitDir)
	cmd.Dir = r.gitDir
	if work != "" {
		cmd.Env = append(cmd.Env, "GIT_WORK_TREE="+work)
		cmd.Dir = work
	}
	if index != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+index)
	}
	return output(cmd, stdin)
}

// output runs cmd with stdin on its standard input and returns its
// standard output, or an error that holds its standard error.
func output(cmd *exec.Cmd, stdin []byte) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// checkTree refuses a tree that is not the full name of a tree object in
// the repository, so that a name such as HEAD cannot stand for a tree.
func (r repository) checkTree(ctx context.Context, tree string) error {
	if !regexp.MustCompile(fmt.Sprintf("^[0-9a-f]{%d}$", r.hashLength)).MatchString(tree) {
		return fmt.Errorf("%q is not the name of a tree", tree)
	}
	out, err := r.run(exec.CommandContext(ctx, "git", "cat-file", "--batch-check=%(objecttype)"), "", "", []byte(tree+"\n"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "tree" {
		return fmt.Errorf("the repository has no tree %s", tree)
	}
	return nil
}

// checkout checks the tree out into the new directory work, with the
// index file index, which must be outside work, and gives the workspace a
// .git of its own with one commit of the tree (decision 0247).
func (r repository) checkout(ctx context.Context, tree, work, index string) error {
	if err := r.checkTree(ctx, tree); err != nil {
		return err
	}
	// The raw diff from the empty tree lists every file of the tree, with
	// its mode and object, which git update-index takes on standard input.
	raw, err := r.run(exec.CommandContext(ctx, "git", "diff-tree", "--stdin", "-r", "-z", "--no-renames"),
		"", "", []byte(r.emptyTree+" "+tree+"\n"))
	if err != nil {
		return err
	}
	info, err := indexInfo(raw)
	if err != nil {
		return err
	}
	if err := os.Mkdir(work, 0o750); err != nil {
		return err
	}
	if _, err := r.run(exec.CommandContext(ctx, "git", "update-index", "-z", "--index-info"), work, index, info); err != nil {
		return err
	}
	if _, err := r.run(exec.CommandContext(ctx, "git", "checkout-index", "--all", "--quiet"), work, index, nil); err != nil {
		return err
	}
	if err := r.workspaceGit(ctx, tree, work, info); err != nil {
		return fmt.Errorf("the workspace's .git could not be made: %w", err)
	}
	return nil
}

// The author and committer of the workspace's one commit, and its
// message.
const (
	workspaceCommitName    = "Pumi"
	workspaceCommitEmail   = "pumi@pumi.invalid"
	workspaceCommitMessage = "The input tree of the invocation\n"
)

// workspaceGit makes the .git of the workspace work, which holds the
// files of tree, already checked out, with the index lines info: a new
// repository of the same object format with the objects of tree, an
// index of those files and one commit of tree on the branch main, so that
// git status in the workspace shows no changes (decision 0247).
//
// The repository has no hooks, and its git commands read no configuration
// of the user's or of the system's, so that nothing outside the workspace
// runs. The runner never runs a git command on it afterwards, so nothing
// the agent does in it, such as a commit or a hook, reaches the resulting
// tree or the project's repository.
func (r repository) workspaceGit(ctx context.Context, tree, work string, info []byte) error {
	ws := workspaceRepository{work: work, gitDir: filepath.Join(work, ".git"), objectFormat: r.objectFormat}
	if _, err := ws.run(exec.CommandContext(ctx, "git", "init", "--quiet", "--template=", "--initial-branch=main"), false, nil); err != nil {
		return err
	}
	// The pack of every object of the tree goes from the project's
	// repository to the workspace's, which keeps the pack.
	pack := exec.CommandContext(ctx, "git", "pack-objects", "--revs", "--stdout", "--quiet")
	pack.Env = append(os.Environ(), "GIT_DIR="+r.gitDir)
	pack.Dir = r.gitDir
	pack.Stdin = strings.NewReader(tree + "\n")
	var packErr bytes.Buffer
	pack.Stderr = &packErr
	out, err := pack.StdoutPipe()
	if err != nil {
		return err
	}
	if err := pack.Start(); err != nil {
		return err
	}
	index := ws.command(exec.CommandContext(ctx, "git", "index-pack", "--stdin"), true)
	var indexOut, indexErr bytes.Buffer
	index.Stdin, index.Stdout, index.Stderr = out, &indexOut, &indexErr
	indexRunErr := index.Run()
	if err := pack.Wait(); err != nil {
		return fmt.Errorf("git pack-objects: %w: %s", err, strings.TrimSpace(packErr.String()))
	}
	if indexRunErr != nil {
		return fmt.Errorf("git index-pack: %w: %s", indexRunErr, strings.TrimSpace(indexErr.String()))
	}
	if _, err := ws.run(exec.CommandContext(ctx, "git", "update-index", "-z", "--index-info"), true, info); err != nil {
		return err
	}
	// The index lines have no file times, which git status would take as
	// changes; the refresh reads them from the checked out files.
	if _, err := ws.run(exec.CommandContext(ctx, "git", "update-index", "-q", "--refresh"), true, nil); err != nil {
		return err
	}
	_, err = ws.run(exec.CommandContext(ctx, "git", "commit", "--quiet", "--no-verify", "--allow-empty", "--file=-"),
		true, []byte(workspaceCommitMessage))
	return err
}

// workspaceRepository is the .git of a workspace, on which the runner
// runs git only while it makes it.
type workspaceRepository struct {
	work, gitDir, objectFormat string
}

// command sets up cmd to run on the workspace's repository, with its work
// tree and index when worktree is true, and with no configuration of the
// user's or of the system's.
func (w workspaceRepository) command(cmd *exec.Cmd, worktree bool) *exec.Cmd {
	cmd.Dir = w.work
	cmd.Env = append(os.Environ(),
		"GIT_DIR="+w.gitDir,
		"GIT_DEFAULT_HASH="+w.objectFormat,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME="+workspaceCommitName,
		"GIT_AUTHOR_EMAIL="+workspaceCommitEmail,
		"GIT_COMMITTER_NAME="+workspaceCommitName,
		"GIT_COMMITTER_EMAIL="+workspaceCommitEmail,
	)
	if worktree {
		cmd.Env = append(cmd.Env, "GIT_WORK_TREE="+w.work, "GIT_INDEX_FILE="+filepath.Join(w.gitDir, "index"))
	}
	return cmd
}

// run runs cmd on the workspace's repository with stdin on its standard
// input and returns its standard output.
func (w workspaceRepository) run(cmd *exec.Cmd, worktree bool, stdin []byte) (string, error) {
	return output(w.command(cmd, worktree), stdin)
}

// indexInfo turns the output of git diff-tree --stdin -r -z from the empty
// tree into the input of git update-index -z --index-info. The output
// starts with the input line, ended by a newline; then each file has a
// field ":<old mode> <new mode> <old object> <new object> A" and its path,
// each ended by a NUL.
func indexInfo(raw string) ([]byte, error) {
	_, rest, ok := strings.Cut(raw, "\n")
	if !ok {
		return nil, fmt.Errorf("git diff-tree printed %q", raw)
	}
	fields := strings.Split(strings.TrimSuffix(rest, "\x00"), "\x00")
	if rest == "" {
		fields = nil
	}
	if len(fields)%2 != 0 {
		return nil, errors.New("git diff-tree printed an odd number of fields")
	}
	var info bytes.Buffer
	for i := 0; i < len(fields); i += 2 {
		status := strings.Fields(fields[i])
		if len(status) != 5 || status[4] != "A" {
			return nil, fmt.Errorf("git diff-tree printed the field %q", fields[i])
		}
		fmt.Fprintf(&info, "%s %s\t%s\x00", status[1], status[3], fields[i+1])
	}
	return info.Bytes(), nil
}

// writeTree records every file of the workspace work in the index file
// index, as git add --all does, which leaves out the files that the
// .gitignore files ignore, writes the tree and returns its name. The
// index holds the input tree, from the checkout.
//
// The tree leaves out every .git (decision 0247). git add --all never
// records a .git, but it records a directory that holds a repository with
// a commit as a gitlink, a pointer to that commit. writeTree records the
// files of such a directory instead, leaving out its .git, and keeps each
// gitlink of the input tree as it was, because the agent's commits never
// leave the workspace.
func (r repository) writeTree(ctx context.Context, work, index string) (string, error) {
	inputLinks, err := r.gitlinks(ctx, work, index)
	if err != nil {
		return "", err
	}
	if _, err := r.run(exec.CommandContext(ctx, "git", "add", "--all"), work, index, nil); err != nil {
		return "", err
	}
	links, err := r.gitlinks(ctx, work, index)
	if err != nil {
		return "", err
	}
	nested, restore := compareGitlinks(inputLinks, links)
	if len(nested) > 0 {
		if _, err := r.run(exec.CommandContext(ctx, "git", "update-index", "-z", "--force-remove", "--stdin"),
			work, index, []byte(strings.Join(nested, "\x00")+"\x00")); err != nil {
			return "", err
		}
	}
	if len(restore) > 0 {
		if _, err := r.run(exec.CommandContext(ctx, "git", "update-index", "-z", "--index-info"), work, index, restore); err != nil {
			return "", err
		}
	}
	if err := r.addNested(ctx, work, index, nested); err != nil {
		return "", err
	}
	tree, err := r.run(exec.CommandContext(ctx, "git", "write-tree"), work, index, nil)
	return strings.TrimSpace(tree), err
}

// compareGitlinks compares the gitlinks links of the index after git add
// --all with the gitlinks inputLinks of the input tree. It returns the
// paths of the new gitlinks, each a directory with a repository of its
// own, sorted, and the index lines, for git update-index -z --index-info,
// that set each gitlink of the input tree back to what it was.
func compareGitlinks(inputLinks, links map[string]string) ([]string, []byte) {
	var restore bytes.Buffer
	var nested []string
	for p, entry := range links {
		if old, ok := inputLinks[p]; !ok {
			nested = append(nested, p)
		} else if old != entry {
			restore.WriteString(old + "\x00")
		}
	}
	for p, old := range inputLinks {
		if _, ok := links[p]; !ok {
			restore.WriteString(old + "\x00")
		}
	}
	slices.Sort(nested)
	return nested, restore.Bytes()
}

// gitlinks returns the gitlinks of the index file index by path, each as
// a line of git update-index -z --index-info without its NUL.
func (r repository) gitlinks(ctx context.Context, work, index string) (map[string]string, error) {
	out, err := r.run(exec.CommandContext(ctx, "git", "ls-files", "-z", "--stage"), work, index, nil)
	if err != nil {
		return nil, err
	}
	links := map[string]string{}
	for entry := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		// An entry is "<mode> <object> <stage>\t<path>".
		if !strings.HasPrefix(entry, "160000 ") {
			continue
		}
		meta, p, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("git ls-files printed %q", entry)
		}
		links[p] = fields[0] + " " + fields[1] + "\t" + p
	}
	return links, nil
}

// addNested records in the index file index the files below the
// directories dirs of the workspace work, which hold repositories of
// their own, leaving out every .git and the files that the .gitignore
// files ignore.
func (r repository) addNested(ctx context.Context, work, index string, dirs []string) error {
	files, err := nestedFiles(work, dirs)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	ignored, err := r.ignored(ctx, work, index, files)
	if err != nil {
		return err
	}
	var add bytes.Buffer
	for _, f := range files {
		if !ignored[f] {
			add.WriteString(f + "\x00")
		}
	}
	if add.Len() == 0 {
		return nil
	}
	_, err = r.run(exec.CommandContext(ctx, "git", "update-index", "-z", "--add", "--replace", "--stdin"), work, index, add.Bytes())
	return err
}

// nestedFiles returns the paths of the regular files and symbolic links
// below the directories dirs of the workspace work, leaving out every
// .git.
func nestedFiles(work string, dirs []string) ([]string, error) {
	var files []string
	for _, dir := range dirs {
		err := fs.WalkDir(os.DirFS(work), dir, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case strings.EqualFold(d.Name(), ".git") && d.IsDir():
				return fs.SkipDir
			case strings.EqualFold(d.Name(), ".git"):
				return nil
			case d.Type().IsRegular() || d.Type()&fs.ModeSymlink != 0:
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// ignored returns which of the paths files of the workspace work the
// .gitignore files ignore.
func (r repository) ignored(ctx context.Context, work, index string, files []string) (map[string]bool, error) {
	out, err := r.run(exec.CommandContext(ctx, "git", "check-ignore", "-z", "--no-index", "--stdin"),
		work, index, []byte(strings.Join(files, "\x00")+"\x00"))
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) && exit.ExitCode() == 1 {
		// git check-ignore exits with status 1 when it ignores no path.
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	ignored := map[string]bool{}
	for p := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		ignored[p] = true
	}
	return ignored, nil
}

// changed returns the paths of the files that differ between the trees
// from and to, added, changed or deleted, in git's order.
func (r repository) changed(ctx context.Context, from, to string) ([]string, error) {
	out, err := r.run(exec.CommandContext(ctx, "git", "diff-tree", "--stdin", "-r", "-z", "--name-only", "--no-renames"),
		"", "", []byte(from+" "+to+"\n"))
	if err != nil {
		return nil, err
	}
	_, rest, ok := strings.Cut(out, "\n")
	if !ok {
		return nil, fmt.Errorf("git diff-tree printed %q", out)
	}
	if rest == "" {
		return []string{}, nil
	}
	return strings.Split(strings.TrimSuffix(rest, "\x00"), "\x00"), nil
}
