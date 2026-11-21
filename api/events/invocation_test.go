// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package events

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
)

// document returns an invocation document that Validate accepts.
func document() *runnerv1.Invocation {
	return &runnerv1.Invocation{
		Invocation:     "invocation-1",
		IdempotencyKey: "key-1",
		Component:      &harnessv1.Component{Kind: harnessv1.Component_KIND_CHECK, Name: "tests", Version: "1"},
		Input:          &harnessv1.Input{Repo: "repo", Tree: "tree-1"},
		Role:           harnessv1.Role_ROLE_GATE,
	}
}

// requested returns an invocation.requested event whose data is data,
// which Validate has not checked.
func requested(t *testing.T, data *eventsv1.InvocationRequested) *cloudeventsv1.CloudEvent {
	t.Helper()
	packed, err := anypb.New(data)
	if err != nil {
		t.Fatal(err)
	}
	e := valid(t)
	e.Type = TypeInvocationRequested
	e.Data = &cloudeventsv1.CloudEvent_ProtoData{ProtoData: packed}
	return e
}

func TestInvocationRequestedCarriesItsDocument(t *testing.T) {
	data, err := InvocationRequested(document())
	if err != nil {
		t.Fatal(err)
	}
	if data.GetInvocation() != "invocation-1" || data.GetIdempotencyKey() != "key-1" {
		t.Errorf("the data names %q and %q, want the document's invocation and key", data.GetInvocation(), data.GetIdempotencyKey())
	}
	if want := HashOf(data.GetDocument()); data.GetDocumentHash() != want || !strings.HasPrefix(want, "sha256:") {
		t.Errorf("the document hash is %q, want %q", data.GetDocumentHash(), want)
	}
	if err := Validate(requested(t, data)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	doc, err := Document(data)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(doc, document()) {
		t.Errorf("Document = %v, want %v", doc, document())
	}
}

func TestInvocationRequestedRefusesABadDocument(t *testing.T) {
	tests := []struct {
		name   string
		change func(*eventsv1.InvocationRequested)
		want   string
	}{
		{"no document", func(d *eventsv1.InvocationRequested) { d.Document = nil }, "document"},
		{"another hash", func(d *eventsv1.InvocationRequested) { d.DocumentHash = HashOf([]byte("other")) }, "hash"},
		{"a document that does not parse", func(d *eventsv1.InvocationRequested) {
			d.Document = []byte{0xff, 0xff}
			d.DocumentHash = HashOf(d.Document)
		}, "document"},
		{"another invocation", func(d *eventsv1.InvocationRequested) { d.Invocation = "invocation-2" }, "invocation-2"},
		{"another idempotency key", func(d *eventsv1.InvocationRequested) { d.IdempotencyKey = "key-2" }, "key-2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := InvocationRequested(document())
			if err != nil {
				t.Fatal(err)
			}
			tt.change(data)
			err = Validate(requested(t, data))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate = %v, want an error that names %q", err, tt.want)
			}
		})
	}
}

func TestInvocationRequestedRefusesAnIncompleteDocument(t *testing.T) {
	for name, change := range map[string]func(*runnerv1.Invocation){
		"no invocation":        func(d *runnerv1.Invocation) { d.Invocation = "" },
		"no role":              func(d *runnerv1.Invocation) { d.Role = harnessv1.Role_ROLE_UNSPECIFIED },
		"no kind of component": func(d *runnerv1.Invocation) { d.Component.Kind = harnessv1.Component_KIND_UNSPECIFIED },
		"no component":         func(d *runnerv1.Invocation) { d.Component = nil },
		"no idempotency key":   func(d *runnerv1.Invocation) { d.IdempotencyKey = "" },
	} {
		t.Run(name, func(t *testing.T) {
			doc := document()
			change(doc)
			if _, err := InvocationRequested(doc); err == nil {
				t.Fatalf("InvocationRequested with %s succeeded, want an error", name)
			}
			raw, err := proto.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			data := &eventsv1.InvocationRequested{
				Invocation: doc.GetInvocation(), IdempotencyKey: doc.GetIdempotencyKey(), Document: raw, DocumentHash: HashOf(raw),
			}
			if err := Validate(requested(t, data)); err == nil {
				t.Fatalf("Validate of a document with %s succeeded, want an error", name)
			}
		})
	}
}
