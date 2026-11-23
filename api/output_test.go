// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package api

import (
	"io/fs"
	"os"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestOutputOfEveryTopLevelMessage(t *testing.T) {
	schemas := os.DirFS(jsonSchemaDir)
	for _, name := range topLevelMessages(t) {
		mt, schema, err := Output(name)
		if err != nil {
			t.Errorf("Output(%q): %v", name, err)
			continue
		}
		if got := mt.Descriptor().FullName(); got != protoreflect.FullName(name) {
			t.Errorf("Output(%q) gives the type of %s", name, got)
		}
		want, err := fs.ReadFile(schemas, name+".jsonschema.bundle.json")
		if err != nil {
			t.Fatal(err)
		}
		if schema != string(want) {
			t.Errorf("Output(%q) gives a JSON Schema other than the one make generate wrote", name)
		}
	}
}

func TestOutputRefuses(t *testing.T) {
	for _, name := range []string{
		"",
		"pumi.workflow.v1.NoSuchMessage",
		// A message from outside the api module.
		"google.protobuf.Duration",
		// A nested message, for which make generate writes no JSON Schema.
		"pumi.events.v1.InvocationResult.Usage",
		"../jsonschema/pumi.workflow.v1.ImplementationOutput",
		"pumi.workflow.v1.ImplementationOutput.jsonschema.bundle",
	} {
		if mt, _, err := Output(name); err == nil {
			t.Errorf("Output(%q) = %v, want an error", name, mt.Descriptor().FullName())
		}
	}
}
