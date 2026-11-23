// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"getpumi.dev/pumi/internal/kernel/workflow"
)

func TestParsePlanReadsTheFrontMatter(t *testing.T) {
	src := "---\n" +
		"plan: PL-02\n" +
		"change: a-humans-work-counts\n" +
		"domain: plan-execution\n" +
		"paths:\n" +
		"  - tools/regime/internal/runner/early_test.go\n" +
		"  - docs/a.md\n" +
		"requirements: []\n" +
		"base_commit: 5120cea038a9cccd5caff4985842dc30f81788d7\n" +
		"inputs:\n" +
		"  - openspec/changes/a-humans-work-counts/proposal.md\n" +
		"---\n\n# PL-02\n"
	p, err := parsePlan([]byte(src))
	if err != nil {
		t.Fatalf("parsePlan: %v", err)
	}
	if p.ID != "a-humans-work-counts-pl-02" {
		t.Errorf("ID = %q, want %q", p.ID, "a-humans-work-counts-pl-02")
	}
	if p.Base != "5120cea038a9cccd5caff4985842dc30f81788d7" {
		t.Errorf("Base = %q", p.Base)
	}
	if want := []string{"tools/regime/internal/runner/early_test.go", "docs/a.md"}; !slices.Equal(p.Paths, want) {
		t.Errorf("Paths = %q, want %q", p.Paths, want)
	}
	if string(p.Text) != src {
		t.Errorf("Text is not the whole file")
	}
}

func TestParsePlanRefusesAPlanWithoutWhatTheRunNeeds(t *testing.T) {
	for name, src := range map[string]string{
		"no front matter":   "# PL-01\n",
		"unended":           "---\nplan: PL-01\n",
		"no base commit":    "---\nplan: PL-01\nchange: c\npaths:\n  - a\n---\n",
		"short base commit": "---\nplan: PL-01\nchange: c\npaths:\n  - a\nbase_commit: 5120cea\n---\n",
		"no paths":          "---\nplan: PL-01\nchange: c\npaths: []\nbase_commit: 5120cea038a9cccd5caff4985842dc30f81788d7\n---\n",
		"no plan":           "---\nchange: c\npaths:\n  - a\nbase_commit: 5120cea038a9cccd5caff4985842dc30f81788d7\n---\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parsePlan([]byte(src)); err == nil {
				t.Errorf("parsePlan(%q) = nil error, want one", src)
			}
		})
	}
}

func TestTheSpikeTaskGivesThePlanFile(t *testing.T) {
	wf, err := workflow.Load(os.DirFS(filepath.Join(workflowsDir, "spike")), "workflow.txtpb")
	if err != nil {
		t.Fatal(err)
	}
	task, ok := wf.Task("implement")
	if !ok {
		t.Fatal("the spike workflow has no task implement")
	}
	if !strings.Contains(task, "`"+planFile+"`") {
		t.Errorf("the task does not name the plan file %s:\n%s", planFile, task)
	}
}
