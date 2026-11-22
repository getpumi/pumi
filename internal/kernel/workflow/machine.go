// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package workflow

import (
	"context"
	"fmt"

	workflowv1 "getpumi.dev/pumi/api/gen/pumi/workflow/v1"
	"github.com/qmuntal/stateless"
)

// Machine is the state machine of one plan. The plan's state is kept
// outside the machine: the kernel projects it from the log, and the
// machine reads it with load and writes it with store (decision 0018).
type Machine struct {
	w  *Workflow
	sm *stateless.StateMachine
	// load reads the plan's state.
	load func(context.Context) (string, error)
}

// NewMachine returns a machine for one plan on the workflow, whose state
// load reads and store writes. The stateless machine has each superstate
// as a superstate of its substates, one internal trigger for each
// transition, which is the transition's index, and no guards, so that it
// never sees two allowed transitions at once (decision 0169).
func (w *Workflow) NewMachine(load func(context.Context) (string, error), store func(context.Context, string) error) *Machine {
	sm := stateless.NewStateMachineWithExternalStorage(
		func(ctx context.Context) (stateless.State, error) { return load(ctx) },
		func(ctx context.Context, s stateless.State) error {
			name, ok := s.(string)
			if !ok {
				return fmt.Errorf("the state %v is not a state name", s)
			}
			return store(ctx, name)
		},
		stateless.FiringImmediate,
	)
	for _, s := range w.def.GetStates() {
		cfg := sm.Configure(s.GetName())
		if p := s.GetParent(); p != "" {
			cfg.SubstateOf(p)
		}
		for _, i := range w.own[s.GetName()] {
			tr := w.transitions[i]
			if tr.Target == s.GetName() {
				cfg.PermitReentry(i)
			} else {
				cfg.Permit(i, tr.Target)
			}
		}
	}
	return &Machine{w: w, sm: sm, load: load}
}

// state reads the plan's state and checks that the workflow has it.
func (m *Machine) state(ctx context.Context) (*workflowv1.State, error) {
	name, err := m.load(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot read the plan's state: %w", err)
	}
	s, ok := m.w.states[name]
	if !ok {
		return nil, fmt.Errorf("the plan is in the state %q, which the workflow %s %s does not have", name, m.w.def.GetName(), m.w.def.GetVersion())
	}
	return s, nil
}

// available returns the transitions that apply in s, in the order the
// kernel decides them: the state's own, then those of each superstate
// outward, each in file order. A final state has none.
func (m *Machine) available(s *workflowv1.State) []Transition {
	if _, final := s.GetEntry().(*workflowv1.State_Final); final {
		return nil
	}
	out := []Transition{}
	for at := s.GetName(); at != ""; at = m.w.states[at].GetParent() {
		for _, i := range m.w.own[at] {
			out = append(out, m.w.transitions[i])
		}
	}
	return out
}

// Transitions returns the transitions for trigger in the plan's state, in
// the order the kernel decides them (decision 0169): the state's own, then
// those of each superstate outward, each in file order. A final state has
// none.
func (m *Machine) Transitions(ctx context.Context, trigger Trigger) ([]Transition, error) {
	s, err := m.state(ctx)
	if err != nil {
		return nil, err
	}
	out := []Transition{}
	for _, tr := range m.available(s) {
		if tr.Trigger == trigger {
			out = append(out, tr)
		}
	}
	return out, nil
}

// Fire fires the transition with the index, which must apply in the
// plan's state, and stores the target state. The kernel calls it only for
// the transition that the control plane allowed (decision 0169).
func (m *Machine) Fire(ctx context.Context, index int) error {
	if index < 0 || index >= len(m.w.transitions) {
		return fmt.Errorf("the workflow has no transition %d", index)
	}
	s, err := m.state(ctx)
	if err != nil {
		return err
	}
	if _, final := s.GetEntry().(*workflowv1.State_Final); final {
		return fmt.Errorf("the plan is in the final state %q, from which no transition fires", s.GetName())
	}
	tr := m.w.transitions[index]
	found := false
	for _, a := range m.available(s) {
		found = found || a.Index == index
	}
	if !found {
		return fmt.Errorf("transition %d, of state %q, does not apply in the state %q", index, tr.State, s.GetName())
	}
	if err := m.sm.FireCtx(ctx, index); err != nil {
		return fmt.Errorf("cannot fire transition %d of state %q to %q: %w", index, tr.State, tr.Target, err)
	}
	return nil
}
