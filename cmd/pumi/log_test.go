// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
)

// The tests of pumi log and of pumi status through the socket
// (getpumi/pumi#98).

// TestStatusThroughTheSocketShowsTheInitialState: pumi status with no
// configuration file, through the socket of pumi run, shows a plan that
// started and fired no transition in the workflow's initial state, as pumi
// status with the configuration file does.
func TestStatusThroughTheSocketShowsTheInitialState(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterWaits)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitEvent(r, events.TypeInvocationStarted)
	if row := c.status()["a1"]; row.state != "implementing" {
		t.Errorf("pumi status through the socket shows a1 in %q while it runs, want the initial state implementing", row.state)
	}
	if row := c.status("--config", c.config)["a1"]; row.state != "implementing" {
		t.Errorf("pumi status with the configuration file shows a1 in %q while it runs, want implementing", row.state)
	}
	c.must("cancel", "a1")
	c.waitState(r, "a1", "cancelled")
}

// TestLogPrintsTheRecentOutputOfARunningInvocation: while pumi run runs
// an invocation, pumi log prints its recent output through the socket.
func TestLogPrintsTheRecentOutputOfARunningInvocation(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterWaits)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitEvent(r, events.TypeInvocationStarted)
	want := testAdapterPrefix + "runs a1/1 on the tree "
	deadline := time.Now().Add(commandWait)
	for {
		out := c.must("log", "a1/1")
		if strings.Contains(out, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pumi log a1/1 wrote %q after %s, want the adapter's line %q; the output of pumi run:\n%s",
				out, commandWait, want, r.out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.must("cancel", "a1")
	c.waitState(r, "a1", "cancelled")
}

// TestLogPrintsTheStoredLog: pumi log prints the full log that the
// invocation's result stores in the blob store, through the socket while
// pumi run runs and from the log opened in its own process after it
// stopped.
func TestLogPrintsTheStoredLog(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterSucceeds)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitState(r, "a1", "merged")
	results := ofType(c.log(), events.TypeInvocationResult)
	if len(results) != 1 {
		t.Fatalf("the log has %d invocation.result events, want 1", len(results))
	}
	hash := dataOf(t, results[0], &eventsv1.InvocationResult{}).GetLogBlobHash()
	name, ok := strings.CutPrefix(hash, "sha256:")
	if !ok {
		t.Fatalf("the result's log_blob_hash is %q, want a sha256: hash", hash)
	}
	root, err := os.OpenRoot(filepath.Join(c.dir, ".git", "pumi"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	stored, err := root.ReadFile(filepath.Join("blobs", "local", name))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), testAdapterPrefix+"runs a1/1") {
		t.Fatalf("the stored log is %q, want the adapter's line", stored)
	}
	if out := c.must("log", "a1/1"); out != string(stored) {
		t.Errorf("pumi log a1/1 through the socket wrote %q, want the stored log %q", out, stored)
	}
	r.stop(t)
	if out := c.must("log", "--config", c.config, "a1/1"); out != string(stored) {
		t.Errorf("pumi log a1/1 with no run wrote %q, want the stored log %q", out, stored)
	}
}

// requestAsKernel appends, as the kernel, the request of an invocation of
// plan named invocation to the clone's log, with no pumi run, so that the
// invocation has no log until a runner runs it.
func (c *commandClone) requestAsKernel(plan, invocation string) {
	c.t.Helper()
	cfg, err := readConfig(c.config)
	if err != nil {
		c.t.Fatal(err)
	}
	log, err := embeddedlog.Open(c.t.Context(), c.dir)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	svc, err := controlplane.New(log, cfg.plane())
	if err != nil {
		c.t.Fatal(err)
	}
	kernel, _, err := newClients(svc)
	if err != nil {
		c.t.Fatal(err)
	}
	doc := proto.CloneOf(cfg.file.GetBase())
	doc.Invocation, doc.IdempotencyKey, doc.Role = invocation, "key-"+invocation, harnessv1.Role_ROLE_AGENT
	doc.Component.Kind = harnessv1.Component_KIND_AGENT_RUN
	data, err := events.InvocationRequested(plan, doc)
	if err != nil {
		c.t.Fatal(err)
	}
	e, err := events.New(data, events.Attributes{ID: "request-" + invocation, Source: "/kernel", Subject: plan, Tenant: "local"})
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := kernel.Append(c.t.Context(), &controlplanev1.AppendRequest{Events: []*cloudeventsv1.CloudEvent{e}}); err != nil {
		c.t.Fatal(err)
	}
}

// TestLogSaysWhenThereIsNoLog: pumi log says so when the invocation has
// no log yet, because it was requested and has not run, or because it runs
// and has sent no output, and fails for an invocation that the log does
// not have.
func TestLogSaysWhenThereIsNoLog(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterWaits)
	c.must("add", "--config", c.config, "--track", "a", "a1")
	c.requestAsKernel("a1", "a1/1")
	if out := c.must("log", "--config", c.config, "a1/1"); out != "The invocation a1/1 has no log yet.\n" {
		t.Errorf("pumi log of a requested invocation wrote %q, want that it has no log yet", out)
	}
	if out, err := c.pumi("log", "--config", c.config, "nope"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("pumi log of an invocation the log does not have = %q, %v; want an error that names it", out, err)
	}
}

// TestLogOfARunningInvocationWithNoOutputSaysSo: an invocation whose lease
// is current and whose runner sent no output, as after pumi run stopped
// while it ran, has no output to print yet.
func TestLogOfARunningInvocationWithNoOutputSaysSo(t *testing.T) {
	c := newCommandClone(t, "tracks", adapterWaits)
	r := c.start()
	c.must("add", "--track", "a", "a1")
	c.waitEvent(r, events.TypeInvocationStarted)
	r.stop(t)
	if out := c.must("log", "--config", c.config, "a1/1"); out != "The invocation a1/1 is running and has sent no output yet.\n" {
		t.Errorf("pumi log of a running invocation with no output wrote %q, want that it has sent none", out)
	}
}
