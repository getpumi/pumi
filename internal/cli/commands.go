// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
)

// waiting is the state that pumi status shows for a plan that has not
// started, because the plans it depends on have not merged or no slot is
// free (decision 0189).
const waiting = "waiting"

// plan is what the log says of one plan, for pumi status.
type plan struct {
	name, track string
	// state is the state the plan's last transition.fired entered, or
	// empty.
	state string
	// requested is whether the log requests an invocation of the plan.
	requested bool
	// escalation is the id of the plan's open escalation.raised, and
	// question its question.
	escalation, question string
}

// readPlans returns the plans of the log, in the order they were added.
func readPlans(ctx context.Context, s *Session) ([]*plan, map[string]*plan, error) {
	res, err := s.Client.Query(ctx, &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Types: []string{
		events.TypePlanAdded, events.TypeTransitionFired, events.TypeInvocationRequested,
		events.TypeEscalationRaised, events.TypeEscalationResolved,
	}}})
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read the log: %w", err)
	}
	var order []*plan
	byName := map[string]*plan{}
	resolved := map[string]bool{}
	for _, entry := range res.GetEvents() {
		if err := readEvent(entry.GetEvent(), &order, byName, resolved); err != nil {
			return nil, nil, err
		}
	}
	for _, p := range order {
		if resolved[p.escalation] {
			p.escalation, p.question = "", ""
		}
	}
	return order, byName, nil
}

// readEvent adds what e says of a plan.
func readEvent(e *cloudeventsv1.CloudEvent, order *[]*plan, byName map[string]*plan, resolved map[string]bool) error {
	data, err := events.Data(e.GetType())
	if err != nil {
		return err
	}
	if err := e.GetProtoData().UnmarshalTo(data); err != nil {
		return fmt.Errorf("the data of the event %s: %w", e.GetId(), err)
	}
	switch d := data.(type) {
	case *eventsv1.PlanAdded:
		if _, ok := byName[d.GetPlan()]; !ok {
			p := &plan{name: d.GetPlan(), track: d.GetTrack()}
			byName[p.name] = p
			*order = append(*order, p)
		}
	case *eventsv1.TransitionFired:
		if p, ok := byName[d.GetPlan()]; ok {
			p.state = d.GetToState()
		}
	case *eventsv1.InvocationRequested:
		if p, ok := byName[d.GetPlan()]; ok {
			p.requested = true
		}
	case *eventsv1.EscalationRaised:
		if p, ok := byName[d.GetPlan()]; ok {
			p.escalation, p.question = e.GetId(), d.GetQuestion()
		}
	case *eventsv1.EscalationResolved:
		resolved[d.GetEscalation()] = true
	}
	return nil
}

// stateOf returns the state that pumi status shows for p: the state its
// last transition entered, or waiting before it started, or else the
// workflow's initial state.
func stateOf(p *plan, initial string) string {
	switch {
	case p.state != "":
		return p.state
	case !p.requested:
		return waiting
	}
	return initial
}

// status writes each plan's track, state and open escalation, one plan to
// a line in the order the plans were added.
func status(ctx context.Context, s *Session, _ []string, w io.Writer) error {
	plans, _, err := readPlans(ctx, s)
	if err != nil {
		return err
	}
	wf, err := s.View.GetWorkflow(ctx, &controlplanev1.GetWorkflowRequest{})
	if err != nil {
		return fmt.Errorf("cannot read the workflow: %w", err)
	}
	initial := wf.GetWorkflow().GetInitial()
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "PLAN\tTRACK\tSTATE\tESCALATION")
	for _, p := range plans {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.name, p.track, stateOf(p, initial), oneLine(p.question))
	}
	return tw.Flush()
}

// oneLine returns text with its white space, newlines included, joined to
// single spaces.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// attributes returns the attributes of an event of the person about the
// plan, with a new id. The control plane sets its tenant from the person's
// identity.
func attributes(s *Session, plan string) events.Attributes {
	return events.Attributes{ID: "person-" + rand.Text(), Source: "/person/" + s.Person, Subject: plan, Tenant: "person"}
}

// appendAsPerson appends e, the result of events.New, as the person.
func appendAsPerson(ctx context.Context, s *Session, e *cloudeventsv1.CloudEvent, err error) error {
	if err != nil {
		return err
	}
	_, err = s.Client.Append(ctx, &controlplanev1.AppendRequest{Events: []*cloudeventsv1.CloudEvent{e}})
	return err
}

// addFlags are the flags of pumi add.
type addFlags struct {
	track     string
	dependsOn plans
}

// plans is a flag that may be given more than once, each time with one
// plan or a comma-separated list.
type plans []string

func (p *plans) String() string { return strings.Join(*p, ",") }

func (p *plans) Set(v string) error {
	for name := range strings.SplitSeq(v, ",") {
		if name = strings.TrimSpace(name); name != "" {
			*p = append(*p, name)
		}
	}
	return nil
}

// define defines the flags of pumi add.
func (a *addFlags) define(fs *flag.FlagSet) {
	fs.StringVar(&a.track, "track", "", "the track the plan joins (required)")
	fs.Var(&a.dependsOn, "depends-on", "a plan of the same track that the plan depends on; may be given more than once")
}

// add appends the plan.added of the plan as the person (decisions 0196 and
// 0198). The control plane refuses a plan that was added before and a
// dependency that is not an earlier plan of the same track.
func (a *addFlags) add(ctx context.Context, s *Session, args []string, w io.Writer) error {
	if a.track == "" {
		return fmt.Errorf("--track names no track")
	}
	name := args[0]
	e, err := events.New(&eventsv1.PlanAdded{Plan: name, Track: a.track, DependsOn: a.dependsOn}, attributes(s, name))
	if err = appendAsPerson(ctx, s, e, err); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Added the plan %s to the track %s.\n", name, a.track)
	return err
}

// cancelPlan appends the person's cancel.requested of the plan, which the
// control plane decides with cancel before it appends it; the kernel then
// cancels the plan and asks its running invocation to stop (decision
// 0192).
func cancelPlan(ctx context.Context, s *Session, args []string, w io.Writer) error {
	name := args[0]
	_, byName, err := readPlans(ctx, s)
	if err != nil {
		return err
	}
	if _, ok := byName[name]; !ok {
		return fmt.Errorf("the log has no plan %s", name)
	}
	e, err := events.New(&eventsv1.CancelRequested{Plan: name, RequestedBy: s.Person}, attributes(s, name))
	if err = appendAsPerson(ctx, s, e, err); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Asked to cancel the plan %s.\n", name)
	return err
}

// answer appends the person's escalation.resolved of the plan's open
// escalation, with the arguments after the plan as the answer.
func answer(ctx context.Context, s *Session, args []string, w io.Writer) error {
	name, text := args[0], strings.Join(args[1:], " ")
	_, byName, err := readPlans(ctx, s)
	if err != nil {
		return err
	}
	p, ok := byName[name]
	switch {
	case !ok:
		return fmt.Errorf("the log has no plan %s", name)
	case p.escalation == "":
		return fmt.Errorf("the plan %s has no open escalation", name)
	}
	data := &eventsv1.EscalationResolved{Escalation: p.escalation, Person: s.Person, Answer: text}
	e, err := events.New(data, attributes(s, name))
	if err = appendAsPerson(ctx, s, e, err); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Answered the escalation %s of the plan %s.\n", p.escalation, name)
	return err
}

// printLog writes the log of the invocation to w: its recent output while
// it runs, with a note to notes that it is not the whole log, or else its
// stored full log (decision 0232), or a line that says it has none yet.
func printLog(ctx context.Context, s *Session, invocation string, w, notes io.Writer) error {
	stream, err := s.View.ReadLog(ctx, &controlplanev1.ReadLogRequest{Invocation: invocation})
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	source, written := controlplanev1.ReadLogResponse_SOURCE_UNSPECIFIED, 0
	for {
		res, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(res.GetOutput()) == 0 {
			source = res.GetSource()
			continue
		}
		if written == 0 && res.GetSource() == controlplanev1.ReadLogResponse_SOURCE_RECENT {
			_, _ = fmt.Fprintf(notes, "The invocation %s is running; this is its recent output.\n", invocation)
		}
		source = res.GetSource()
		n, err := w.Write(res.GetOutput())
		written += n
		if err != nil {
			return err
		}
	}
	switch {
	case written > 0:
		return nil
	case source == controlplanev1.ReadLogResponse_SOURCE_RECENT:
		_, err = fmt.Fprintf(w, "The invocation %s is running and has sent no output yet.\n", invocation)
	case source == controlplanev1.ReadLogResponse_SOURCE_STORED:
		_, err = fmt.Fprintf(w, "The stored log of the invocation %s is empty.\n", invocation)
	default:
		_, err = fmt.Fprintf(w, "The invocation %s has no log yet.\n", invocation)
	}
	return err
}
