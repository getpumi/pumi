// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package cli

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"

	"getpumi.dev/pumi/api/events"
	controlplanev1 "getpumi.dev/pumi/api/gen/pumi/controlplane/v1"
	eventsv1 "getpumi.dev/pumi/api/gen/pumi/events/v1"
)

// switchedRoles returns the last switch of each model role that is on its
// fallback, in the order of those switches: the role's last
// model_role.switched in the log switched it to its fallback, naming the
// invocation that a limit stopped (decision 0246).
func switchedRoles(ctx context.Context, s *Session) ([]*eventsv1.ModelRoleSwitched, error) {
	res, err := s.Client.Query(ctx, &controlplanev1.QueryRequest{Filter: &controlplanev1.Filter{Types: []string{events.TypeModelRoleSwitched}}})
	if err != nil {
		return nil, fmt.Errorf("cannot read the log: %w", err)
	}
	last := map[string]*eventsv1.ModelRoleSwitched{}
	var order []string
	for _, entry := range res.GetEvents() {
		d := &eventsv1.ModelRoleSwitched{}
		if err := entry.GetEvent().GetProtoData().UnmarshalTo(d); err != nil {
			return nil, fmt.Errorf("the data of the event %s: %w", entry.GetEvent().GetId(), err)
		}
		order = append(slices.DeleteFunc(order, func(r string) bool { return r == d.GetRole() }), d.GetRole())
		last[d.GetRole()] = d
	}
	var out []*eventsv1.ModelRoleSwitched
	for _, role := range order {
		if d := last[role]; d.GetInvocation() != "" {
			out = append(out, d)
		}
	}
	return out, nil
}

// writeSwitchedRoles writes, after an empty line, each model role that is
// on its fallback, with the adapter and model it is on and the limit and
// invocation that switched it, and how a person switches it back; it
// writes nothing when no role is on its fallback (decision 0246).
func writeSwitchedRoles(w io.Writer, switched []*eventsv1.ModelRoleSwitched) error {
	if len(switched) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "MODEL ROLE ON ITS FALLBACK\tADAPTER\tMODEL\tSWITCHED WHEN THE LIMIT STOPPED")
	for _, d := range switched {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s on %s %s\n", d.GetRole(), d.GetAdapter(), d.GetModel(),
			d.GetInvocation(), d.GetLimit().GetAdapter(), d.GetLimit().GetModel())
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, "pumi role reset <role> switches a model role back.")
	return err
}

// resetRole appends the person's model_role.switched that switches the
// model role back from its fallback to the adapter and model that the
// limit stopped, which its last switch names (decision 0246). It refuses a
// role that is not on its fallback.
func resetRole(ctx context.Context, s *Session, args []string, w io.Writer) error {
	role := args[0]
	switched, err := switchedRoles(ctx, s)
	if err != nil {
		return err
	}
	var last *eventsv1.ModelRoleSwitched
	for _, d := range switched {
		if d.GetRole() == role {
			last = d
		}
	}
	if last == nil {
		return fmt.Errorf("the model role %q is not on its fallback, so there is nothing to switch back", role)
	}
	back := &eventsv1.ModelRoleSwitched{
		Role: role, Adapter: last.GetLimit().GetAdapter(), Model: last.GetLimit().GetModel(),
		Limit: last.GetLimit(), SwitchedBy: s.Person,
	}
	e, err := events.New(back, events.Attributes{ID: "person-" + rand.Text(), Source: "/person/" + s.Person, Subject: role, Tenant: "person"})
	if err = appendAsPerson(ctx, s, e, err); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Switched the model role %s back from %s on %s to %s on %s.\n",
		role, last.GetModel(), last.GetAdapter(), back.GetModel(), back.GetAdapter())
	return err
}
