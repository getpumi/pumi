// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Command license runs the dependency license check (decisions 0075 to 0078)
// on the core module in the working directory, which is what make license
// does. It prints one line for each module that passes, with its licenses,
// and one line for each finding. With -notices file, which make notices
// passes, it also writes THIRD_PARTY_NOTICES to file when the check passes
// (decision 0079). It exits with status 1 when any module fails, and with
// status 2 when it cannot run.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"getpumi.dev/pumi/internal/checks/license"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// errUsage is returned for wrong arguments. run prints it without the
// "license: " prefix.
var errUsage = errors.New("usage: license [-notices file]")

// run returns the exit status: 0 when every module passes, 1 when any fails,
// and 2 when the check cannot run or cannot write its output. The messages
// on stderr are the last report of a result that the exit status already
// carries, so an error from writing them is not checked.
func run(args []string, stdout, stderr io.Writer) int {
	failed, err := check(context.Background(), args, stdout)
	switch {
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "license: %v\n", err)
		return 2
	case failed > 0:
		_, _ = fmt.Fprintf(stderr, "license: findings against %s and %s: %d\n", license.PolicyPath, license.ExceptionsPath, failed)
		return 1
	}
	return 0
}

// check writes the checked modules and the findings to stdout, and a summary
// line when there is no finding, and then writes the notices if asked. It
// returns the number of findings.
func check(ctx context.Context, args []string, stdout io.Writer) (int, error) {
	flags := flag.NewFlagSet("license", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	notices := flags.String("notices", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 0, errUsage
	}
	res, err := license.Run(ctx, ".")
	if err != nil {
		return 0, err
	}
	var out []string
	for _, c := range res.Checked {
		line := fmt.Sprintf("license: %s %s: %s", c.Module.Path, c.Module.Version, strings.Join(c.Licenses, ", "))
		if c.Exception != nil {
			line += " (exception: " + c.Exception.Reason + ")"
		}
		out = append(out, line)
	}
	for _, f := range res.Findings {
		out = append(out, "license: "+f.String())
	}
	if len(res.Findings) == 0 {
		out = append(out, fmt.Sprintf("license: modules checked: %d, none fails", len(res.Checked)))
	}
	if err := writeLines(stdout, out); err != nil {
		return 0, err
	}
	if len(res.Findings) > 0 || *notices == "" {
		return len(res.Findings), nil
	}
	data, err := license.Notices(res.Checked)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(*notices), 0o750); err != nil {
		return 0, err
	}
	if err := os.WriteFile(*notices, data, 0o600); err != nil {
		return 0, err
	}
	return 0, writeLines(stdout, []string{"license: wrote " + *notices})
}

func writeLines(w io.Writer, lines []string) error {
	for _, line := range lines {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}
