// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package api

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	configv1 "getpumi.dev/pumi/api/gen/pumi/config/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// jsonSchemaDir is the directory that make generate writes the JSON Schemas
// to (decision 0160).
const jsonSchemaDir = "gen/jsonschema"

// schemaFiles are the files of the api module's protobuf schema, one of each
// package, from which the generator writes JSON Schemas.
var schemaFiles = []protoreflect.FileDescriptor{
	cloudeventsv1.File_io_cloudevents_v1_cloudevents_proto,
	configv1.File_pumi_config_v1_config_proto,
	controlplanev1.File_pumi_controlplane_v1_controlplane_proto,
	eventsv1.File_pumi_events_v1_invocations_proto,
	factsv1.File_pumi_facts_v1_code_proto,
	harnessv1.File_pumi_harness_v1_harness_proto,
	runnerv1.File_pumi_runner_v1_runner_proto,
	workflowv1.File_pumi_workflow_v1_workflow_proto,
}

// topLevelMessages returns the full names of the top-level messages of every
// file of the packages of schemaFiles.
func topLevelMessages(t *testing.T) []string {
	t.Helper()
	names := []string{}
	for _, file := range schemaFiles {
		pkg := file.Package()
		dir := filepath.Join("proto", filepath.FromSlash(strings.ReplaceAll(string(pkg), ".", "/")))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".proto") {
				continue
			}
			path := strings.ReplaceAll(string(pkg), ".", "/") + "/" + e.Name()
			fd, err := registryFile(path)
			if err != nil {
				t.Fatalf("%s is not registered: %v", path, err)
			}
			for i := range fd.Messages().Len() {
				names = append(names, string(fd.Messages().Get(i).FullName()))
			}
		}
	}
	slices.Sort(names)
	return names
}

func TestEveryTopLevelMessageHasAJSONSchema(t *testing.T) {
	want := []string{}
	for _, name := range topLevelMessages(t) {
		want = append(want, name+".jsonschema.bundle.json")
	}
	schemas := os.DirFS(jsonSchemaDir)
	entries, err := fs.ReadDir(schemas, ".")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s holds\n%q\nwant one self-contained JSON Schema for each top-level message\n%q", jsonSchemaDir, got, want)
	}
	for _, name := range got {
		data, err := fs.ReadFile(schemas, name)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			ID     string                     `json:"$id"`
			Schema string                     `json:"$schema"`
			Ref    string                     `json:"$ref"`
			Defs   map[string]json.RawMessage `json:"$defs"`
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		def := strings.TrimSuffix(name, ".bundle.json") + ".json"
		if schema.ID != name || schema.Schema != "https://json-schema.org/draft/2020-12/schema" || schema.Ref != "#/$defs/"+def || schema.Defs[def] == nil {
			t.Errorf("%s is not a JSON Schema 2020-12 bundle of its message: $id %q, $schema %q, $ref %q", name, schema.ID, schema.Schema, schema.Ref)
		}
	}
}

// registryFile returns the registered file descriptor at path.
func registryFile(path string) (protoreflect.FileDescriptor, error) {
	return protoregistry.GlobalFiles.FindFileByPath(path)
}
