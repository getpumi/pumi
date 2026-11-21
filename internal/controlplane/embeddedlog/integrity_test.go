// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package embeddedlog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
)

// remarshal returns line with its event changed by change.
func remarshal(t *testing.T, line []byte, change func(*cloudeventsv1.CloudEvent)) []byte {
	t.Helper()
	var e cloudeventsv1.CloudEvent
	if err := protojson.Unmarshal(line, &e); err != nil {
		t.Fatal(err)
	}
	change(&e)
	b, err := protojson.Marshal(&e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// setAttr sets the string attribute name of e.
func setAttr(e *cloudeventsv1.CloudEvent, name, value string) {
	e.Attributes[name] = &cloudeventsv1.CloudEvent_CloudEventAttributeValue{
		Attr: &cloudeventsv1.CloudEvent_CloudEventAttributeValue_CeString{CeString: value},
	}
}

// writeFile replaces the file at path with b.
func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// joinLines joins lines, each ended by a newline.
func joinLines(lines [][]byte) []byte {
	var b bytes.Buffer
	for _, line := range lines {
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func TestOpenStopsOnADamagedLogAndRepairsNothing(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage func(t *testing.T, pumi string, lines [][]byte)
		want   string
	}{
		{"a line that does not parse", func(t *testing.T, pumi string, lines [][]byte) {
			lines[1] = []byte(`{"id":`)
			writeFile(t, filepath.Join(pumi, "log.jsonl"), joinLines(lines))
		}, "log.jsonl line 2:"},
		{"a line that is not a Pumi event", func(t *testing.T, pumi string, lines [][]byte) {
			lines[1] = remarshal(t, lines[1], func(e *cloudeventsv1.CloudEvent) { e.Type = "com.example.other" })
			writeFile(t, filepath.Join(pumi, "log.jsonl"), joinLines(lines))
		}, "log.jsonl line 2:"},
		{"another tenant", func(t *testing.T, pumi string, lines [][]byte) {
			lines[0] = remarshal(t, lines[0], func(e *cloudeventsv1.CloudEvent) { setAttr(e, events.ExtensionTenant, "other") })
			writeFile(t, filepath.Join(pumi, "log.jsonl"), joinLines(lines))
		}, "log.jsonl line 1:"},
		{"a first event with a prevhash", func(t *testing.T, pumi string, lines [][]byte) {
			lines[0] = remarshal(t, lines[0], func(e *cloudeventsv1.CloudEvent) {
				setAttr(e, events.ExtensionPrevHash, hashOf([]byte("nothing")))
			})
			writeFile(t, filepath.Join(pumi, "log.jsonl"), joinLines(lines))
		}, "log.jsonl line 1:"},
		{"a broken prevhash", func(t *testing.T, pumi string, lines [][]byte) {
			lines[1] = remarshal(t, lines[1], func(e *cloudeventsv1.CloudEvent) { e.Id = "changed" })
			writeFile(t, filepath.Join(pumi, "log.jsonl"), joinLines(lines))
		}, "log.jsonl line 3:"},
		{"a committed line with no newline", func(t *testing.T, pumi string, lines [][]byte) {
			b := joinLines(lines)
			writeFile(t, filepath.Join(pumi, "log.jsonl"), b[:len(b)-1])
		}, "log.jsonl line 3:"},
		{"a head after the end of the log", func(t *testing.T, pumi string, lines [][]byte) {
			writeFile(t, filepath.Join(pumi, "log.jsonl"), joinLines(lines[:1]))
		}, "log.jsonl line 2:"},
		{"a head whose hash does not match", func(t *testing.T, pumi string, _ [][]byte) {
			writeFile(t, filepath.Join(pumi, "head"), []byte(`{"position":2,"hash":"`+hashOf([]byte("other"))+`"}`+"\n"))
		}, "log.jsonl line 2:"},
		{"a head that does not parse", func(t *testing.T, pumi string, _ [][]byte) {
			writeFile(t, filepath.Join(pumi, "head"), []byte(`{"position":`))
		}, "head:"},
		{"no log.jsonl", func(t *testing.T, pumi string, _ [][]byte) {
			if err := os.Remove(filepath.Join(pumi, "log.jsonl")); err != nil {
				t.Fatal(err)
			}
		}, "log.jsonl line 1:"},
		{"no head", func(t *testing.T, pumi string, _ [][]byte) {
			if err := os.Remove(filepath.Join(pumi, "head")); err != nil {
				t.Fatal(err)
			}
		}, "head:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := newClone(t)
			l := open(t, dir)
			mustAppend(t, l, planAdded(t, "e1", "plan-1"), planAdded(t, "e2", "plan-1"))
			mustAppend(t, l, planAdded(t, "e3", "plan-1"))
			pumi := l.Dir()
			closeLog(t, l)
			c.damage(t, pumi, readLines(t, pumi))
			before := snapshot(t, pumi)

			l, err := Open(t.Context(), dir)
			if err == nil {
				_ = l.Close()
				t.Fatal("Open succeeded on a damaged log")
			}
			if !strings.Contains(err.Error(), filepath.Join(pumi, c.want)) {
				t.Errorf("Open returned %q, want an error that names %s", err, filepath.Join(pumi, c.want))
			}
			if after := snapshot(t, pumi); after != before {
				t.Errorf("Open changed the files of a damaged log:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// snapshot returns the names and contents of log.jsonl and head in pumi.
func snapshot(t *testing.T, pumi string) string {
	t.Helper()
	var b strings.Builder
	for _, name := range []string{"log.jsonl", "head"} {
		content, err := openRoot(t, pumi).ReadFile(name)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		b.WriteString(name + "=" + string(content) + ";")
	}
	return b.String()
}
