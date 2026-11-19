// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Command imports runs the import graph check (decision 0108) on the core
// module in the working directory, with the rules and allowlists in
// .pumi/imports.json, which is what make imports does. It prints one line for
// each import that breaks a rule, then one line for each binary under cmd
// with the number of third-party modules it links, which decision 0064 tracks
// without a limit. It exits with status 1 when any import breaks a rule, and
// with status 2 when it cannot run.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"getpumi.dev/pumi/internal/checks/imports"
)

// configPath is the configuration file, relative to the root of the core
// module (decision 0112).
const configPath = ".pumi/imports.json"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// errUsage is returned for wrong arguments. run prints it without the
// "imports: " prefix.
var errUsage = errors.New("usage: imports")

// run returns the exit status: 0 when no import breaks a rule, 1 when any
// does, and 2 when the check cannot run or cannot write its findings. The
// messages on stderr are the last report of a result that the exit status
// already carries, so an error from writing them is not checked.
func run(args []string, stdout, stderr io.Writer) int {
	failed, err := check(context.Background(), args, stdout)
	switch {
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "imports: %v\n", err)
		return 2
	case failed > 0:
		_, _ = fmt.Fprintf(stderr, "imports: findings against the rules of %s: %d\n", configPath, failed)
		return 1
	}
	return 0
}

// check writes the findings and the binaries to stdout, and a summary line
// when there is no finding. It returns the number of findings.
func check(ctx context.Context, args []string, stdout io.Writer) (int, error) {
	if len(args) != 0 {
		return 0, errUsage
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, err
	}
	cfg, err := imports.ParseConfig(data)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", configPath, err)
	}
	res, err := imports.Check(ctx, ".", cfg)
	if err != nil {
		return 0, err
	}
	var out []string
	for _, f := range res.Findings {
		out = append(out, f.String())
	}
	for _, b := range res.Binaries {
		out = append(out, formatBinary(b))
	}
	if len(res.Findings) == 0 {
		out = append(out, fmt.Sprintf("imports: packages checked: %d, none fails", res.Packages))
	}
	for _, line := range out {
		if _, err := fmt.Fprintln(stdout, line); err != nil {
			return 0, err
		}
	}
	return len(res.Findings), nil
}

// formatBinary formats the third-party modules of one binary as one line.
func formatBinary(b imports.Binary) string {
	return fmt.Sprintf("imports: %s: third-party modules: %s, %s", b.Path, count(b.Direct, "direct"), count(b.Indirect, "indirect"))
}

// count returns the number of modules and the word what, followed by their
// paths in parentheses when there are any.
func count(mods []string, what string) string {
	if len(mods) == 0 {
		return "0 " + what
	}
	return fmt.Sprintf("%d %s (%s)", len(mods), what, strings.Join(mods, ", "))
}
