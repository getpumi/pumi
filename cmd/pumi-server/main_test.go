// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunWritesNameAndVersion(t *testing.T) {
	var buf bytes.Buffer
	if err := run(&buf); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got := buf.String(); !strings.HasPrefix(got, "pumi-server ") || !strings.HasSuffix(got, "\n") {
		t.Errorf("run() wrote %q, want a line starting with %q", got, "pumi-server ")
	}
}
