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
	svc := controlplane.New(openLog(t))
	for name, id := range map[string]controlplane.Identity{
		"no caller":         {Tenant: embeddedlog.Tenant},
		"an unknown caller": {Caller: "adapter", Tenant: embeddedlog.Tenant},
		"no tenant":         {Caller: controlplane.CallerKernel},
		"another tenant":    {Caller: controlplane.CallerPerson, Tenant: "other"},
	} {
		if _, err := svc.Server(id); err == nil {
			t.Errorf("Server with %s succeeded, want an error", name)
		}
	}
	for _, caller := range []controlplane.Caller{controlplane.CallerKernel, controlplane.CallerPerson, controlplane.CallerRunner} {
		if _, err := svc.Server(controlplane.Identity{Caller: caller, Tenant: embeddedlog.Tenant}); err != nil {
			t.Errorf("Server for a %s: %v", caller, err)
		}
	}
}
