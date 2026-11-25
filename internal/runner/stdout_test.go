// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"strings"
	"testing"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

func TestTheStandardOutputIsReadLineByLineAcrossWrites(t *testing.T) {
	var figures []string
	s := &stdoutReader{usage: func(u *harnessv1.UsageFigure) { figures = append(figures, u.GetModel()) }}
	out := `{"usage":{"model":"a"}}` + "\n\n" + `{"usage":{"model":"b"}}` + "\n" + `{"result":{"status":"STATUS_COMPLETED"}}`
	// One byte at a time, so that every line arrives in pieces.
	for i := range len(out) {
		if _, err := s.Write([]byte{out[i]}); err != nil {
			t.Fatal(err)
		}
		if i == strings.Index(out, "\n\n") && len(figures) != 1 {
			t.Fatalf("after the first line the figures are %q, want a", figures)
		}
	}
	res, err := s.end()
	if err != nil || res.GetStatus() != harnessv1.Result_STATUS_COMPLETED {
		t.Fatalf("end() = %v, %v; want the completed result", res, err)
	}
	if strings.Join(figures, ",") != "a,b" {
		t.Errorf("the figures are %q, want a and b", figures)
	}
}

func TestALineThatBreaksTheProtocolNamesItsNumber(t *testing.T) {
	for name, out := range map[string]string{
		"does not parse":   `{"usage":{}}` + "\n" + `{"status":"STATUS_COMPLETED"}` + "\n",
		"holds neither":    `{"usage":{}}` + "\n" + `{}` + "\n",
		"after the result": `{"usage":{}}` + "\n" + `{"result":{"status":"STATUS_COMPLETED"}}` + "\n" + `{"usage":{}}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			s := &stdoutReader{usage: func(*harnessv1.UsageFigure) {}}
			if _, err := s.Write([]byte(out)); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"does not parse": "line 2 ", "holds neither": "line 2 ", "after the result": "line 3 "}[name]
			if _, err := s.end(); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("end() = %v, want an error naming the %s", err, want)
			}
		})
	}
}
