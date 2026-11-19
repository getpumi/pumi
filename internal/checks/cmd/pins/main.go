// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Command pins is the pin report (decision 0115) on the repository in the
// working directory, which is what make pins does. It asks the Go module
// proxy, proxy.golang.org unless -proxy names another, about newer releases
// of golangci-lint, Trivy, Go and the tool modules, and prints one line for
// each pin. It exits with status 1 when a pin is out of date, and with
// status 2 when it cannot run.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"getpumi.dev/pumi/internal/checks/pins"
)

// timeout limits each request to the proxy.
const timeout = 30 * time.Second

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// errUsage is returned for wrong arguments. run prints it without the
// "pins: " prefix.
var errUsage = errors.New("usage: pins [-proxy url]")

// run returns the exit status: 0 when every pin is current or not yet due, 1
// when any is out of date, and 2 when the report cannot run. The messages on
// stderr are the last report of a result that the exit status already
// carries, so an error from writing them is not checked.
func run(args []string, stdout, stderr io.Writer) int {
	outOfDate, err := report(context.Background(), args, stdout)
	switch {
	case errors.Is(err, errUsage):
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	case err != nil:
		_, _ = fmt.Fprintf(stderr, "pins: %v\n", err)
		return 2
	case outOfDate > 0:
		_, _ = fmt.Fprintf(stderr, "pins: out of date: %d; update each in a pull request of its own (decision 0116)\n", outOfDate)
		return 1
	}
	return 0
}

// report writes one line for each pin to stdout, and a summary line when no
// pin is out of date. It returns the number of pins that are out of date.
func report(ctx context.Context, args []string, stdout io.Writer) (int, error) {
	flags := flag.NewFlagSet("pins", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	proxyURL := flags.String("proxy", pins.DefaultProxy, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 0, errUsage
	}
	proxy := &pins.Proxy{BaseURL: *proxyURL, Client: &http.Client{Timeout: timeout}}
	list, err := pins.Run(ctx, ".", proxy, time.Now())
	if err != nil {
		return 0, err
	}
	outOfDate := 0
	for _, p := range list {
		if p.OutOfDate {
			outOfDate++
		}
		if _, err := fmt.Fprintln(stdout, "pins: "+p.String()); err != nil {
			return 0, err
		}
	}
	if outOfDate == 0 {
		if _, err := fmt.Fprintf(stdout, "pins: pins checked: %d, none is out of date\n", len(list)); err != nil {
			return 0, err
		}
	}
	return outOfDate, nil
}
