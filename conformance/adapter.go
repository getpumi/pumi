// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package conformance

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// API is a provider API that a harness calls.
type API int

const (
	// APIMessages is Anthropic's Messages API.
	APIMessages API = iota + 1
	// APIResponses is OpenAI's Responses API.
	APIResponses
)

func (a API) String() string {
	switch a {
	case APIMessages:
		return "Messages API"
	case APIResponses:
		return "Responses API"
	}
	return fmt.Sprintf("API(%d)", int(a))
}

// Mode is a credential mode of an adapter (decision 0240).
type Mode int

const (
	// ModeAPIKey is the API key mode: every call goes through the model
	// gateway, which the suite plays, with the invocation token.
	ModeAPIKey Mode = iota + 1
	// ModeSignIn is the sign-in mode: the harness talks to the provider
	// directly with the person's own sign-in, and the adapter passes on
	// the usage that its harness reports (decision 0241).
	ModeSignIn
)

func (m Mode) String() string {
	switch m {
	case ModeAPIKey:
		return "API key mode"
	case ModeSignIn:
		return "sign-in mode"
	}
	return fmt.Sprintf("Mode(%d)", int(m))
}

// Item is an item of the capability list whose refusal a scenario checks
// (decision 0159).
type Item int

const (
	// ItemWritePaths is the paths the run may write.
	ItemWritePaths Item = iota + 1
	// ItemCommands is the commands the run may run.
	ItemCommands
	// ItemNetwork is the hosts the run may reach.
	ItemNetwork
	// ItemToolRead is the kind of tool that reads files.
	ItemToolRead
	// ItemToolEdit is the kind of tool that edits files.
	ItemToolEdit
	// ItemToolRunCommands is the kind of tool that runs commands.
	ItemToolRunCommands
	// ItemToolWeb is the kind of tool that uses the web.
	ItemToolWeb
)

func (i Item) String() string {
	switch i {
	case ItemWritePaths:
		return "write paths"
	case ItemCommands:
		return "commands"
	case ItemNetwork:
		return "network"
	case ItemToolRead:
		return "the kind of tool that reads"
	case ItemToolEdit:
		return "the kind of tool that edits"
	case ItemToolRunCommands:
		return "the kind of tool that runs commands"
	case ItemToolWeb:
		return "the kind of tool that uses the web"
	}
	return fmt.Sprintf("Item(%d)", int(i))
}

// StepKind is what a step of a scenario asks the harness to do.
type StepKind int

const (
	// StepWrite asks the harness to write Content to the file Path,
	// creating it.
	StepWrite StepKind = iota + 1
	// StepRead asks the harness to read the file Path.
	StepRead
	// StepRun asks the harness to run Command with a shell in the
	// workspace.
	StepRun
	// StepFetch asks the harness to fetch URL with its web tool.
	StepFetch
	// StepOutput asks the harness to give Output, the JSON of the
	// invocation's output message, as its structured output.
	StepOutput
)

func (k StepKind) String() string {
	switch k {
	case StepWrite:
		return "write"
	case StepRead:
		return "read"
	case StepRun:
		return "run"
	case StepFetch:
		return "fetch"
	case StepOutput:
		return "output"
	}
	return fmt.Sprintf("StepKind(%d)", int(k))
}

// Step is one thing that a scenario asks the harness to do, written
// without any harness in mind. Adapter.Turn turns it into the model's
// answer that makes the harness do it.
type Step struct {
	Kind StepKind
	// Path is the file of a write or a read: relative to the workspace,
	// which is the harness's working directory, or absolute.
	Path string
	// Content is what a write writes.
	Content string
	// Command is the command of a run, as one line for a shell.
	Command string
	// URL is the URL of a fetch.
	URL string
	// Output is the structured output, as JSON.
	Output string
}

// Turn is the model's answer to one call: text and the tool calls that
// the harness is to make. An answer with no tool calls ends the harness's
// run.
type Turn struct {
	Text  string
	Calls []ToolCall
}

// ToolCall is one tool call of the model's answer, in the harness's own
// terms, such as Claude Code's Write or Codex's exec_command.
type ToolCall struct {
	// Name is the name of the tool.
	Name string
	// Input is the tool's input: a JSON object, or, when Freeform is set,
	// the raw text of a custom tool of the Responses API, such as Codex's
	// apply_patch.
	Input    string
	Freeform bool
}

// Adapter describes the adapter under test and the harness's side of the
// scenarios.
type Adapter struct {
	// Path is the adapter's executable. The suite starts it as the runner
	// does: in the workspace, with the invocation on its standard input.
	Path string
	// API is the provider API that the adapter's harness calls.
	API API
	// Modes are the credential modes that the adapter offers, the one in
	// which most scenarios run first.
	Modes []Mode
	// Turn turns a scenario step into the model's answer that makes the
	// harness do it. The suite gives each tool call its id.
	Turn func(Step) Turn
	// SignIn returns the environment that points the harness at the
	// provider at baseURL with the person's own sign-in credential, as a
	// test of the adapter sets it up. The suite uses it in the sign-in
	// mode; in the API key mode it sets the same environment with another
	// address and checks that no call goes there or carries the
	// credential. It is needed for ModeSignIn.
	SignIn func(baseURL, credential string) []string
	// Env is more environment for every run of the adapter, such as a
	// PATH that holds the pinned harness.
	Env []string
	// NestedSession is the environment that the harness's nested-session
	// guard reads when it runs inside a session of itself, such as
	// CLAUDECODE=1 for Claude Code (regime PEX-042).
	NestedSession []string
	// ReportsRefusals says that the harness reports what its settings
	// refused, so that the adapter's result names each refusal (decision
	// 0249).
	ReportsRefusals bool
	// ReportsLimits says that the harness reports a provider's usage or
	// rate limit, so that the adapter's result has STATUS_LIMITED.
	ReportsLimits bool
	// Unenforced are the items of the capability list that the harness
	// cannot refuse, each with the reason; the runner's confinement check
	// covers them (decision 0159), and the suite skips their scenarios.
	Unenforced map[Item]string
	// Timeout is how long one run of the adapter may take; zero is two
	// minutes.
	Timeout time.Duration
	// StopGrace is how long the adapter may take to stop after its
	// standard input closes; zero is 30 seconds.
	StopGrace time.Duration
}

// DefaultTimeout and DefaultStopGrace are the times of an Adapter that
// leaves them zero.
const (
	DefaultTimeout   = 2 * time.Minute
	DefaultStopGrace = 30 * time.Second
)

// validate rejects an adapter that the suite cannot run.
func (a *Adapter) validate() error {
	switch {
	case a.Path == "":
		return errors.New("conformance: the adapter has no Path")
	case a.API != APIMessages && a.API != APIResponses:
		return fmt.Errorf("conformance: the adapter's API is %v, not APIMessages or APIResponses", a.API)
	case len(a.Modes) == 0:
		return errors.New("conformance: the adapter offers no credential mode")
	case a.Turn == nil:
		return errors.New("conformance: the adapter has no Turn")
	case slices.Contains(a.Modes, ModeSignIn) && a.SignIn == nil:
		return errors.New("conformance: the adapter offers the sign-in mode but has no SignIn")
	}
	for _, m := range a.Modes {
		if m != ModeAPIKey && m != ModeSignIn {
			return fmt.Errorf("conformance: the adapter offers %v, which is not a credential mode", m)
		}
	}
	return nil
}

func (a *Adapter) timeout() time.Duration {
	if a.Timeout > 0 {
		return a.Timeout
	}
	return DefaultTimeout
}

func (a *Adapter) stopGrace() time.Duration {
	if a.StopGrace > 0 {
		return a.StopGrace
	}
	return DefaultStopGrace
}
