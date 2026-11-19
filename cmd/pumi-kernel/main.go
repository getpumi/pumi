// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Command pumi-kernel hosts the kernel in remote mode.
//
// A main package under cmd/ is the only kind of package that may import more
// than one component, and only to wire them together. For now it prints its
// name and version and exits.
package main

import (
	"fmt"
	"io"
	"os"

	"getpumi.dev/pumi/internal/shared"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(w io.Writer) error {
	return shared.WriteVersion(w, "pumi-kernel")
}
