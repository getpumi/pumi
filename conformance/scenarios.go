// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"fmt"
	"net/url"
	"path/filepath"

	"getpumi.dev/pumi/api"
	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// scenario is one scenario of the suite.
type scenario struct {
	name string
	// mode is the credential mode the scenario needs; zero runs it in the
	// adapter's first mode.
	mode Mode
	// item is the item of the capability list whose refusal the scenario
	// checks, which is skipped when the adapter's harness cannot refuse
	// it; zero for none.
	item Item
	// limits says that the scenario needs a harness that reports limits.
	limits   bool
	behavior behavior
	// status is the result's status that the scenario wants.
	status harnessv1.Result_Status
	// output is the invocation's output schema; nil for none.
	output *outputSchema
	// caps returns the capability list; nil is no tools and the gateway or
	// the provider only.
	caps func(w *world) *harnessv1.Capabilities
	// steps returns the steps; nil is none.
	steps func(w *world) []planned
	// env returns more environment for the adapter.
	env func(a *Adapter) []string
	// check checks more after the run.
	check func(t T, w *world, o *outcome)
}

// outputSchema is the output schema of an invocation: the message and its
// JSON Schema, which make generate wrote (decision 0160).
type outputSchema struct {
	message, schema string
}

// secondOpinion is the output of the output-schema scenario: a message of
// the api module with a nested message, so its JSON Schema has $defs.
var secondOpinion = &workflowv1.SecondOpinion{Answers: []*workflowv1.SecondOpinion_Answer{
	{QuestionId: "q1", Answer: "The conformance suite asked for this answer."},
}}

// secondOpinionSchema returns the output schema of secondOpinion.
func secondOpinionSchema() *outputSchema {
	name := string(secondOpinion.ProtoReflect().Descriptor().FullName())
	_, schema, err := api.Output(name)
	if err != nil {
		panic(fmt.Sprintf("conformance: %v", err))
	}
	return &outputSchema{message: name, schema: schema}
}

// caps returns a capability list with the kinds of tool tools, the write
// paths paths and the commands commands, and the gateway or the provider
// only.
func caps(tools []harnessv1.Capabilities_ToolKind, paths, commands []string) *harnessv1.Capabilities {
	return &harnessv1.Capabilities{
		WritePaths: paths, Commands: commands, Tools: tools,
		Network: &harnessv1.Capabilities_Network{Access: harnessv1.Capabilities_Network_ACCESS_GATEWAY},
	}
}

// withHosts returns c with network access to hosts.
func withHosts(c *harnessv1.Capabilities, hosts ...string) *harnessv1.Capabilities {
	c.Network = &harnessv1.Capabilities_Network{Access: harnessv1.Capabilities_Network_ACCESS_HOSTS, Hosts: hosts}
	return c
}

const (
	read  = harnessv1.Capabilities_TOOL_KIND_READ
	edit  = harnessv1.Capabilities_TOOL_KIND_EDIT
	run   = harnessv1.Capabilities_TOOL_KIND_RUN_COMMANDS
	web   = harnessv1.Capabilities_TOOL_KIND_WEB
	ended = harnessv1.Result_STATUS_COMPLETED
)

// content is what each write writes.
const content = "Written by a Pumi conformance run.\n"

// write is a write of path, refused or not, with the refusal that a
// harness that reports refusals names when refusal is set.
func write(path string, refused, refusal bool) planned {
	p := planned{step: Step{Kind: StepWrite, Path: path, Content: content}, refused: refused, target: path}
	if refusal {
		p.refusal = &harnessv1.Refusal{Tool: edit, Asked: &harnessv1.Refusal_Path{Path: path}}
	}
	return p
}

// command is a run of command, which makes target, refused or not, with
// the refusal that a harness that reports refusals names when refusal is
// set.
func command(command, target string, refused, refusal bool) planned {
	p := planned{step: Step{Kind: StepRun, Command: command}, refused: refused, target: target}
	if refusal {
		p.refusal = &harnessv1.Refusal{Tool: run, Asked: &harnessv1.Refusal_Command{Command: command}}
	}
	return p
}

// fetch is a refused fetch of the world's host, with the refusal that a
// harness that reports refusals names when refusal is set.
func (w *world) fetch(refusal bool) planned {
	p := planned{step: Step{Kind: StepFetch, URL: w.host.server.URL + "/pumi-conformance-fetch"}, refused: true}
	if refusal {
		p.refusal = &harnessv1.Refusal{Tool: web, Asked: &harnessv1.Refusal_Host{Host: hostOf(w.host.server.URL)}}
	}
	return p
}

// scenarios are the scenarios of the suite, in the order Run runs them.
var scenarios = []scenario{
	{
		// The process contract: a run that completes ends with exit
		// status 0 and the result as the last line (decisions 0158 and
		// 0252).
		name: "result", behavior: answerScript, status: ended,
	},
	{
		// The structured output parses strictly into the message that
		// the output schema names (decision 0160).
		name: "output-schema", status: ended, output: secondOpinionSchema(),
		steps: func(*world) []planned {
			out, err := protojson.Marshal(secondOpinion)
			if err != nil {
				panic(err)
			}
			return []planned{{step: Step{Kind: StepOutput, Output: string(out)}}}
		},
		check: checkOutput,
	},
	{
		// A call that the provider refuses for good fails the run, with a
		// result.
		name: "failed", behavior: rejectCalls, status: harnessv1.Result_STATUS_FAILED,
	},
	{
		// Closing standard input stops the run in the middle of a call
		// (decision 0158).
		name: "stop", behavior: holdCall, status: harnessv1.Result_STATUS_STOPPED,
	},
	{
		name: "write-paths", item: ItemWritePaths, status: ended,
		caps: func(*world) *harnessv1.Capabilities {
			return caps([]harnessv1.Capabilities_ToolKind{read, edit}, []string{"src/**"}, nil)
		},
		steps: func(*world) []planned {
			return []planned{write("src/allowed.txt", false, false), write("docs/refused.txt", true, true)}
		},
	},
	{
		// A write path is anchored to the workspace: src/** does not
		// allow a src directory deeper down, or outside it (regime
		// PEX-007).
		name: "anchored-paths", item: ItemWritePaths, status: ended,
		caps: func(*world) *harnessv1.Capabilities {
			return caps([]harnessv1.Capabilities_ToolKind{read, edit}, []string{"src/**"}, nil)
		},
		steps: func(w *world) []planned {
			return []planned{
				write("src/nested/allowed.txt", false, false),
				write("lib/src/refused.txt", true, true),
				write(filepath.Join(w.outside, "src", "refused.txt"), true, true),
			}
		},
	},
	{
		name: "commands", item: ItemCommands, status: ended,
		caps: func(*world) *harnessv1.Capabilities {
			return caps([]harnessv1.Capabilities_ToolKind{run}, []string{"out/**"}, []string{"touch"})
		},
		steps: func(*world) []planned {
			return []planned{
				command("touch out/listed", "out/listed", false, false),
				command("mkdir out/unlisted", "out/unlisted", true, true),
				command("touch out/first && mkdir out/chained", "out/chained", true, true),
			}
		},
	},
	{
		// An entry with an assignment allows that command with exactly
		// that assignment (decision 0248, regime PEX-056).
		name: "assignments", item: ItemCommands, status: ended,
		caps: func(*world) *harnessv1.Capabilities {
			return caps([]harnessv1.Capabilities_ToolKind{run}, []string{"out/**"}, []string{"PUMI_CONFORMANCE=1 touch"})
		},
		steps: func(*world) []planned {
			return []planned{
				command("PUMI_CONFORMANCE=1 touch out/assigned", "out/assigned", false, false),
				command("touch out/bare", "out/bare", true, true),
				command("PUMI_OTHER=1 touch out/other", "out/other", true, true),
				command("PUMI_CONFORMANCE=1 PUMI_OTHER=1 touch out/extra", "out/extra", true, true),
			}
		},
	},
	{
		// An entry without an assignment does not allow the command with
		// one (decision 0248).
		name: "assignments-unlisted", item: ItemCommands, status: ended,
		caps: func(*world) *harnessv1.Capabilities {
			return caps([]harnessv1.Capabilities_ToolKind{run}, []string{"out/**"}, []string{"touch"})
		},
		steps: func(*world) []planned {
			return []planned{
				command("touch out/plain", "out/plain", false, false),
				command("PUMI_CONFORMANCE=1 touch out/assigned", "out/assigned", true, true),
			}
		},
	},
	{
		name: "network", item: ItemNetwork, status: ended,
		caps: func(*world) *harnessv1.Capabilities {
			return withHosts(caps([]harnessv1.Capabilities_ToolKind{read, web}, nil, nil), "pumi-conformance.invalid")
		},
		steps: func(w *world) []planned { return []planned{w.fetch(true)} },
	},
	{
		name: "tool-read", item: ItemToolRead, status: ended,
		caps: func(*world) *harnessv1.Capabilities {
			return caps([]harnessv1.Capabilities_ToolKind{edit}, []string{"src/**"}, nil)
		},
		steps: func(*world) []planned {
			return []planned{{step: Step{Kind: StepRead, Path: secretFile}, refused: true}}
		},
	},
	{
		name: "tool-edit", item: ItemToolEdit, status: ended,
		caps: func(*world) *harnessv1.Capabilities {
			return caps([]harnessv1.Capabilities_ToolKind{read}, []string{"**"}, nil)
		},
		steps: func(*world) []planned { return []planned{write("src/refused.txt", true, false)} },
	},
	{
		name: "tool-run", item: ItemToolRunCommands, status: ended,
		caps: func(*world) *harnessv1.Capabilities {
			return caps([]harnessv1.Capabilities_ToolKind{read}, []string{"**"}, []string{"touch"})
		},
		steps: func(*world) []planned { return []planned{command("touch out/refused", "out/refused", true, false)} },
	},
	{
		name: "tool-web", item: ItemToolWeb, status: ended,
		caps: func(w *world) *harnessv1.Capabilities {
			return withHosts(caps([]harnessv1.Capabilities_ToolKind{read}, nil, nil), hostOf(w.host.server.URL))
		},
		steps: func(w *world) []planned { return []planned{w.fetch(false)} },
	},
	{
		// The adapter runs where its harness's nested-session guard sees
		// a session of the harness (regime PEX-042).
		name: "nested-session", status: ended,
		env: func(a *Adapter) []string { return a.NestedSession },
	},
	{
		// The token and model of every call, in each credential mode,
		// over a run of more than one call.
		name: "calls-api-key", mode: ModeAPIKey, status: ended,
		caps:  readOnly,
		steps: readReadme,
	},
	{
		name: "calls-sign-in", mode: ModeSignIn, status: ended,
		caps:  readOnly,
		steps: readReadme,
	},
	{
		// On the sign-in path, the adapter passes on the usage that its
		// harness reports, with the model and every kind of token
		// (decision 0241).
		name: "usage-sign-in", mode: ModeSignIn, status: ended,
		caps:  readOnly,
		steps: readReadme,
		check: checkUsage,
	},
	{
		// A provider's usage or rate limit ends the run with
		// STATUS_LIMITED, without waiting it out (decision 0249).
		name: "limit", limits: true, behavior: limitCalls, status: harnessv1.Result_STATUS_LIMITED,
	},
}

func readOnly(*world) *harnessv1.Capabilities {
	return caps([]harnessv1.Capabilities_ToolKind{read}, nil, nil)
}

func readReadme(*world) []planned {
	return []planned{{step: Step{Kind: StepRead, Path: "README.md"}}}
}

// checkOutput checks that the structured output parses strictly into the
// output message and is the one the scenario asked for (decision 0160).
func checkOutput(t T, _ *world, o *outcome) {
	t.Helper()
	if o.result == nil {
		return
	}
	got := &workflowv1.SecondOpinion{}
	if err := protojson.Unmarshal([]byte(o.result.GetOutput()), got); err != nil {
		t.Errorf("the structured output %q does not parse as %s: %v (decision 0160)", o.result.GetOutput(), got.ProtoReflect().Descriptor().FullName(), err)
		return
	}
	if !proto.Equal(got, secondOpinion) {
		t.Errorf("the structured output is %v, want %v", got, secondOpinion)
	}
}

// figure is the usage figure that one call the fake provider answered
// adds up to.
func figure(a API) *harnessv1.UsageFigure {
	f := &harnessv1.UsageFigure{
		InputTokens: usageInput, FiveMinuteCacheWriteTokens: usageFiveMinute,
		CacheReadTokens: usageRead, OutputTokens: usageOutput,
	}
	if a == APIMessages {
		f.OneHourCacheWriteTokens = usageOneHour
	}
	return f
}

// checkUsage checks that the usage figures add up, kind by kind, to the
// usage of the calls that the fake provider answered, and that each names
// a model of the role (decision 0241). A harness may report usage per call
// or per turn, so only the totals are compared.
func checkUsage(t T, w *world, o *outcome) {
	t.Helper()
	answered := 0
	for _, c := range w.provider.taken() {
		if c.answered {
			answered++
		}
	}
	if len(o.figures) == 0 {
		t.Errorf("the adapter passed no usage figures on the sign-in path, where the runner counts them (decision 0241)")
		return
	}
	one := figure(w.a.API)
	want := [5]int64{}
	for i, v := range kinds(one) {
		want[i] = v * int64(answered)
	}
	got := [5]int64{}
	for _, f := range o.figures {
		if f.GetModel() != model && f.GetModel() != fallbackModel {
			t.Errorf("a usage figure names the model %q, want %q or %q", f.GetModel(), model, fallbackModel)
		}
		for i, v := range kinds(f) {
			got[i] += v
		}
	}
	if got != want {
		t.Errorf("the usage figures add up to %v tokens (input, five-minute cache write, one-hour cache write, cache read, output), want %v for the %d calls that the fake provider answered", got, want, answered)
	}
}

// kinds returns the tokens of a figure by kind.
func kinds(f *harnessv1.UsageFigure) [5]int64 {
	return [5]int64{f.GetInputTokens(), f.GetFiveMinuteCacheWriteTokens(), f.GetOneHourCacheWriteTokens(), f.GetCacheReadTokens(), f.GetOutputTokens()}
}

// hostOf returns the host of the URL u, without its port.
func hostOf(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		panic(fmt.Sprintf("conformance: %v", err))
	}
	return parsed.Hostname()
}
