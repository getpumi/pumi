// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"getpumi.dev/pumi/api/events"
	cloudeventsv1 "getpumi.dev/pumi/api/gen/io/cloudevents/v1"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
)

// planTrees is what the log says of a plan's trees, for pumi branch.
type planTrees struct {
	track string
	// commit is the base commit of the plan's first invocation.requested
	// (decision 0313).
	commit string
	// tree is the newest tree that a result of the plan's invocations
	// carries whose invocation succeeded, and invocation that invocation
	// (decision 0315).
	tree, invocation string
}

// treeReader reads what the events of the log say of one plan's trees.
type treeReader struct {
	name string
	planTrees
	// added is whether the log adds the plan, and requested whether it
	// requests an invocation of it.
	added, requested bool
	// ours are the plan's invocations, and trees the trees of their
	// results until their invocation.finished.
	ours  map[string]bool
	trees map[string]string
}

// read reads what e says of the plan.
func (r *treeReader) read(e *cloudeventsv1.CloudEvent) error {
	data, err := events.Data(e.GetType())
	if err != nil {
		return err
	}
	if err := e.GetProtoData().UnmarshalTo(data); err != nil {
		return fmt.Errorf("the data of the event %s: %w", e.GetId(), err)
	}
	switch d := data.(type) {
	case *eventsv1.PlanAdded:
		if d.GetPlan() == r.name && !r.added {
			r.added, r.track = true, d.GetTrack()
		}
	case *eventsv1.InvocationRequested:
		return r.request(e, d)
	case *eventsv1.InvocationResult:
		if r.ours[d.GetInvocation()] && d.GetTree() != "" {
			r.trees[d.GetInvocation()] = d.GetTree()
		}
	case *eventsv1.InvocationFinished:
		// Only a result whose invocation succeeded moves the plan's tree
		// (decision 0315).
		tree, ok := r.trees[d.GetInvocation()]
		if ok && d.GetOutcome() == eventsv1.InvocationFinished_OUTCOME_SUCCEEDED {
			r.tree, r.invocation = tree, d.GetInvocation()
		}
	}
	return nil
}

// request reads the invocation.requested e, whose data is d: the plan's
// first holds its base commit (decision 0313).
func (r *treeReader) request(e *cloudeventsv1.CloudEvent, d *eventsv1.InvocationRequested) error {
	if d.GetPlan() != r.name {
		return nil
	}
	r.ours[d.GetInvocation()] = true
	if r.requested {
		return nil
	}
	r.requested = true
	doc, err := events.Document(d)
	if err != nil {
		return fmt.Errorf("the invocation.requested %s: %w", e.GetId(), err)
	}
	r.commit = doc.GetBaseCommit()
	return nil
}

// readTrees returns what the log says of the trees of the plan name, and
// whether the log adds it.
func readTrees(ctx context.Context, s *Session, name string) (planTrees, bool, error) {
	res, err := s.Client.Query(ctx, &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Types: []string{
		events.TypePlanAdded, events.TypeInvocationRequested, events.TypeInvocationResult, events.TypeInvocationFinished,
	}}})
	if err != nil {
		return planTrees{}, false, fmt.Errorf("cannot read the log: %w", err)
	}
	r := &treeReader{name: name, ours: map[string]bool{}, trees: map[string]string{}}
	for _, entry := range res.GetEvents() {
		if err := r.read(entry.GetEvent()); err != nil {
			return planTrees{}, false, err
		}
	}
	return r.planTrees, r.added, nil
}

// writeBranch writes the last tree of the plan args[0] as a commit on the
// new branch args[1], with the plan's base commit as its parent (decisions
// 0289, 0313 and 0314).
func writeBranch(write func(ctx context.Context, b Branch) (string, error)) func(ctx context.Context, s *Session, args []string, w io.Writer) error {
	return func(ctx context.Context, s *Session, args []string, w io.Writer) error {
		name, branch := args[0], args[1]
		if write == nil {
			return errors.New("the wiring writes no branch")
		}
		p, added, err := readTrees(ctx, s, name)
		switch {
		case err != nil:
			return err
		case !added:
			return fmt.Errorf("the log has no plan %s", name)
		case p.tree == "":
			return fmt.Errorf("the plan %s has no result tree: no invocation of it that succeeded changed the tree", name)
		case p.commit == "":
			return fmt.Errorf("the log records no base commit for the plan %s, which started before decision 0313", name)
		}
		commit, err := write(ctx, Branch{
			Name: branch, Tree: p.tree, Parent: p.commit,
			Message: fmt.Sprintf("Plan %s\n\nTrack: %s\nInvocation: %s\n", name, p.track, p.invocation),
		})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "Wrote the tree of %s, the last tree of the plan %s, as the commit %s on the branch %s, with the parent %s.\n",
			p.invocation, name, commit, branch, p.commit)
		return err
	}
}
