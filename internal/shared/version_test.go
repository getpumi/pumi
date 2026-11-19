// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package shared

import (
	"bytes"
	"errors"
	"runtime/debug"
	"testing"
)

func TestVersionFromBuildInfo(t *testing.T) {
	tests := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"stamped version", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, true, "v0.1.0"},
		{"pseudo-version", &debug.BuildInfo{Main: debug.Module{Version: "v0.0.0-20261119171500-0123456789ab+dirty"}}, true, "v0.0.0-20261119171500-0123456789ab+dirty"},
		{"development build", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true, "(devel)"},
		{"empty version", &debug.BuildInfo{}, true, "(devel)"},
		{"no build information", nil, false, "(devel)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionFrom(tt.info, tt.ok); got != tt.want {
				t.Errorf("versionFrom() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWriteVersionNamesTheBinary(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteVersion(&buf, "pumi-kernel"); err != nil {
		t.Fatalf("WriteVersion() error = %v", err)
	}
	want := "pumi-kernel " + Version() + "\n"
	if got := buf.String(); got != want {
		t.Errorf("WriteVersion() wrote %q, want %q", got, want)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestWriteVersionReturnsWriteError(t *testing.T) {
	if err := WriteVersion(failingWriter{}, "pumi"); err == nil {
		t.Error("WriteVersion() error = nil, want the writer's error")
	}
}
