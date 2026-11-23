// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package api

import (
	"embed"
	"fmt"

	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	factsv1 "getpumi.dev/pumi/api/gen/pumi/facts/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// jsonSchemas are the JSON Schemas that make generate writes for the
// top-level messages of the api module (decision 0160).
//
//go:embed gen/jsonschema/*.jsonschema.bundle.json
var jsonSchemas embed.FS

// packages are the protobuf packages of the api module, by one file of
// each. Naming a file also links its package's types into the program, so
// that Output finds them.
var packages = []protoreflect.FileDescriptor{
	cloudeventsv1.File_io_cloudevents_v1_cloudevents_proto,
	controlplanev1.File_pumi_controlplane_v1_controlplane_proto,
	eventsv1.File_pumi_events_v1_invocations_proto,
	factsv1.File_pumi_facts_v1_code_proto,
	harnessv1.File_pumi_harness_v1_harness_proto,
	runnerv1.File_pumi_runner_v1_runner_proto,
	workflowv1.File_pumi_workflow_v1_workflow_proto,
}

// Output returns the type of the top-level message of the api module with
// the full name name, which an invocation document names as its output
// schema, and the JSON Schema that make generate wrote for it, for
// harnesses that take JSON Schema (decision 0160).
func Output(name string) (protoreflect.MessageType, string, error) {
	full := protoreflect.FullName(name)
	if !full.IsValid() {
		return nil, "", fmt.Errorf("api: %q is not the full name of a protobuf message", name)
	}
	mt, err := protoregistry.GlobalTypes.FindMessageByName(full)
	if err != nil {
		return nil, "", fmt.Errorf("api: the api module has no message %s", name)
	}
	if !inPackages(mt.Descriptor().ParentFile().Package()) {
		return nil, "", fmt.Errorf("api: the message %s is not in the api module", name)
	}
	schema, err := jsonSchemas.ReadFile("gen/jsonschema/" + name + ".jsonschema.bundle.json")
	if err != nil {
		return nil, "", fmt.Errorf("api: make generate wrote no JSON Schema for %s, which is not a top-level message", name)
	}
	return mt, string(schema), nil
}

// inPackages reports whether pkg is a protobuf package of the api module.
func inPackages(pkg protoreflect.FullName) bool {
	for _, fd := range packages {
		if fd.Package() == pkg {
			return true
		}
	}
	return false
}
