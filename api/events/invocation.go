// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package events

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	runnerv1 "getpumi.dev/pumi/api/gen/pumi/runner/v1"
)

// HashOf returns sha256: and the hex SHA-256 of b, the form of
// document_hash and of prevhash (decision 0133).
func HashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// InvocationRequested returns the data of an invocation.requested event
// that carries doc, the invocation document, as its binary protobuf, with
// its hash (decision 0165). It refuses a document that Validate would
// refuse.
func InvocationRequested(doc *runnerv1.Invocation) (*eventsv1.InvocationRequested, error) {
	if err := checkDocument(doc); err != nil {
		return nil, err
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encoding the invocation document: %w", err)
	}
	return &eventsv1.InvocationRequested{
		Invocation:     doc.GetInvocation(),
		IdempotencyKey: doc.GetIdempotencyKey(),
		DocumentHash:   HashOf(raw),
		Document:       raw,
	}, nil
}

// Document returns the invocation document that data carries. It refuses
// data with no document, a document whose hash is not document_hash, a
// document that does not parse, that names no invocation, idempotency key,
// role or kind of component, and a document whose invocation or
// idempotency key is not the data's.
func Document(data *eventsv1.InvocationRequested) (*runnerv1.Invocation, error) {
	if len(data.GetDocument()) == 0 {
		return nil, errors.New("the invocation request carries no document (decision 0165)")
	}
	if got := HashOf(data.GetDocument()); got != data.GetDocumentHash() {
		return nil, fmt.Errorf("the invocation document's hash is %s, not the document_hash %q (decision 0165)",
			got, data.GetDocumentHash())
	}
	doc := &runnerv1.Invocation{}
	if err := proto.Unmarshal(data.GetDocument(), doc); err != nil {
		return nil, fmt.Errorf("the invocation document does not parse as pumi.runner.v1.Invocation: %w", err)
	}
	if err := checkDocument(doc); err != nil {
		return nil, err
	}
	if doc.GetInvocation() != data.GetInvocation() {
		return nil, fmt.Errorf("the invocation request names the invocation %q, but its document names %q",
			data.GetInvocation(), doc.GetInvocation())
	}
	if doc.GetIdempotencyKey() != data.GetIdempotencyKey() {
		return nil, fmt.Errorf("the invocation request has the idempotency key %q, but its document has %q",
			data.GetIdempotencyKey(), doc.GetIdempotencyKey())
	}
	return doc, nil
}

// checkDocument refuses a document that names no invocation, idempotency
// key, role or kind of component, which the control plane needs to match
// the invocation with a runner and to apply the append table (decision
// 0156).
func checkDocument(doc *runnerv1.Invocation) error {
	switch {
	case doc.GetInvocation() == "":
		return errors.New("the invocation document names no invocation")
	case doc.GetIdempotencyKey() == "":
		return errors.New("the invocation document has no idempotency key")
	case doc.GetRole() == harnessv1.Role_ROLE_UNSPECIFIED:
		return errors.New("the invocation document names no role")
	case doc.GetComponent().GetKind() == harnessv1.Component_KIND_UNSPECIFIED:
		return errors.New("the invocation document names no kind of component")
	}
	return nil
}
