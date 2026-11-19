// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package license

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Policy is .pumi/licenses.json (decision 0112): the licenses that pass
// (decision 0075) and the licenses that pass only for a module with an
// exception (decision 0078). Every license is an SPDX identifier, as Trivy
// reports it.
type Policy struct {
	Allowed     []string `json:"allowed"`
	ByException []string `json:"by_exception"`
}

// Exception is one entry of .pumi/license-exceptions.json: the module, the
// license it was approved under and the reason (decision 0078). For a module
// that go.mod replaces with another module, the entry names the replacement,
// which is the code the binaries include. JSON has no
// comments, so the reason is a field (decision 0112).
type Exception struct {
	Module  string `json:"module"`
	License string `json:"license"`
	Reason  string `json:"reason"`
}

// decodeStrict decodes one JSON object into v. It rejects unknown fields, so
// a misspelled field is not silently ignored, and data after the object.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("data after the JSON object")
	}
	return nil
}

// ParsePolicy reads .pumi/licenses.json. It requires both lists, a license
// in "allowed", and each license at most once in the two lists together.
func ParsePolicy(data []byte) (*Policy, error) {
	var p Policy
	if err := decodeStrict(data, &p); err != nil {
		return nil, err
	}
	switch {
	case len(p.Allowed) == 0:
		return nil, errors.New(`"allowed" is empty`)
	case p.ByException == nil:
		return nil, errors.New(`"by_exception" is missing`)
	}
	seen := map[string]bool{}
	for _, l := range slices.Concat(p.Allowed, p.ByException) {
		if strings.TrimSpace(l) == "" {
			return nil, errors.New("a license is empty")
		}
		if seen[l] {
			return nil, fmt.Errorf("the license %s is listed twice", l)
		}
		seen[l] = true
	}
	return &p, nil
}

// ParseExceptions reads .pumi/license-exceptions.json. It requires every
// field, each module at most once, and a license that p lets pass by
// exception: an exception for a license that passes anyway is not needed,
// and one for a license that never passes is not possible (decision 0075).
func ParseExceptions(data []byte, p *Policy) ([]Exception, error) {
	var file struct {
		Exceptions []Exception `json:"exceptions"`
	}
	if err := decodeStrict(data, &file); err != nil {
		return nil, err
	}
	if file.Exceptions == nil {
		return nil, errors.New(`"exceptions" is missing`)
	}
	seen := map[string]bool{}
	for i, e := range file.Exceptions {
		for _, f := range []struct{ name, value string }{{"module", e.Module}, {"license", e.License}, {"reason", e.Reason}} {
			if strings.TrimSpace(f.value) == "" {
				return nil, fmt.Errorf("exception %d: %q is empty", i+1, f.name)
			}
		}
		if seen[e.Module] {
			return nil, fmt.Errorf("the module %s is listed twice", e.Module)
		}
		seen[e.Module] = true
		if !slices.Contains(p.ByException, e.License) {
			return nil, fmt.Errorf("exception %d: %s is not a license that passes by exception in .pumi/licenses.json", i+1, e.License)
		}
	}
	return file.Exceptions, nil
}

// PinnedTrivyVersion returns the Trivy version that install-tools.sh pins
// (decision 0086): the value of its one TRIVY_VERSION= line.
func PinnedTrivyVersion(script []byte) (string, error) {
	var versions []string
	sc := bufio.NewScanner(bytes.NewReader(script))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "TRIVY_VERSION="); ok {
			versions = append(versions, v)
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if len(versions) != 1 || versions[0] == "" {
		return "", fmt.Errorf("want one TRIVY_VERSION= line with a version, got %q", versions)
	}
	return versions[0], nil
}
