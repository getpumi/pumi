// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package controlplane_test

import (
	"testing"

	"getpumi.dev/pumi/internal/controlplane"
	"getpumi.dev/pumi/internal/controlplane/embeddedlog"
)

// TestServerRefusesAnIdentityItCannotServe: the wiring gives each client an
// identity (decision 0156), and the embedded control plane serves only the
// callers it knows, in the tenant of its log (decision 0157).
func TestServerRefusesAnIdentityItCannotServe(t *testing.T) {
	svc := newService(t)
	for name, id := range map[string]controlplane.Identity{
		"no caller":         {Principal: "p", Tenant: embeddedlog.Tenant},
		"an unknown caller": {Caller: "adapter", Principal: "p", Tenant: embeddedlog.Tenant},
		"no tenant":         {Caller: controlplane.CallerKernel, Principal: "kernel"},
		"another tenant":    {Caller: controlplane.CallerPerson, Principal: "p", Tenant: "other"},
		"no principal":      {Caller: controlplane.CallerPerson, Tenant: embeddedlog.Tenant},
	} {
		if _, err := svc.Server(id); err == nil {
			t.Errorf("Server with %s succeeded, want an error", name)
		}
	}
	for _, caller := range []controlplane.Caller{controlplane.CallerKernel, controlplane.CallerPerson, controlplane.CallerRunner} {
		if _, err := svc.Server(identity(caller)); err != nil {
			t.Errorf("Server for a %s: %v", caller, err)
		}
	}
}

// TestNewRefusesAWiringItCannotDecideWith: the policies of the wiring must
// parse and validate against the schema, and the project needs a name.
func TestNewRefusesAWiringItCannotDecideWith(t *testing.T) {
	for name, change := range map[string]func(*controlplane.Config){
		"a policy that does not parse": func(c *controlplane.Config) {
			c.Policies = append(c.Policies, controlplane.PolicyFile{Name: "bad.cedar", Text: "permit ("})
		},
		"a policy that reads an undeclared fact": func(c *controlplane.Config) {
			c.Policies = append(c.Policies, controlplane.PolicyFile{
				Name: "bad.cedar",
				Text: "@id(\"bad\")\nforbid (principal, action == Pumi::Action::\"merge\", resource) unless { context.specValid };\n",
			})
		},
		"a project with no name": func(c *controlplane.Config) { c.Project.Name = "" },
	} {
		cfg := config()
		change(&cfg)
		if _, err := controlplane.New(openLog(t), cfg); err == nil {
			t.Errorf("New with %s succeeded, want an error", name)
		}
	}
}
