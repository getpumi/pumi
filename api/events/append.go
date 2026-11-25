// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package events

import "slices"

// Appender is a caller that may append events through the control plane,
// and, for a runner, the call it makes and the invocation it holds
// (decision 0156). Runners append only through RunnerService, and adapters
// never append. No request names its own appender: the control plane takes
// it from the caller's identity.
type Appender string

// The appenders of decision 0156.
const (
	// AppenderKernel is the kernel.
	AppenderKernel Appender = "kernel"
	// AppenderPerson is a person.
	AppenderPerson Appender = "person"
	// AppenderControlPlane is the control plane itself, which appends the
	// decisions of Decide and holds the lease timers.
	AppenderControlPlane Appender = "control plane"
	// AppenderRunnerClaim is a runner's Claim.
	AppenderRunnerClaim Appender = "runner Claim"
	// AppenderRunnerReportAgentRole is a runner's Report for an invocation
	// in the agent role.
	AppenderRunnerReportAgentRole Appender = "runner Report of an agent-role invocation"
	// AppenderRunnerReportGateRole is a runner's Report for an invocation in
	// the gate role.
	AppenderRunnerReportGateRole Appender = "runner Report of a gate-role invocation"
	// AppenderRunnerReportExternalAction is a runner's Report for an
	// external-action invocation, such as the forge adapter's merge.
	AppenderRunnerReportExternalAction Appender = "runner Report of an external-action invocation"
)

// appendTable gives, for each event type, the appenders that may append it
// (decision 0156). A test checks that it lists every event type and no
// other. The control plane also decides writeVerdict, approve, override and
// cancel with Cedar before it appends a verdict, an approval, an override or
// a cancel, so the invariants on them hold even if this table were wrong.
var appendTable = map[string][]Appender{
	// A person adds a plan to a track (decision 0198).
	TypePlanAdded:           {AppenderPerson},
	TypeTransitionFired:     {AppenderKernel},
	TypeInvocationRequested: {AppenderKernel},
	TypeEscalationRaised:    {AppenderKernel},
	// The kernel cancels when a budget or a policy stops a run.
	TypeCancelRequested: {AppenderKernel, AppenderPerson},

	TypeApprovalRecorded:   {AppenderPerson},
	TypeOverrideRecorded:   {AppenderPerson},
	TypeEscalationResolved: {AppenderPerson},
	TypeWarningReviewed:    {AppenderPerson},

	// Claim also writes the lease, which the control plane appends because
	// it holds the lease timers.
	TypeInvocationStarted: {AppenderRunnerClaim},
	// Every Report writes the result and the end of its invocation; an
	// agent-role invocation writes nothing more but a model role's switch.
	TypeInvocationResult: {
		AppenderRunnerReportAgentRole, AppenderRunnerReportGateRole, AppenderRunnerReportExternalAction,
	},
	// The kernel also finishes an external action in doubt that its adapter
	// says happened, with the outcome resolved on recovery (decision 0202).
	TypeInvocationFinished: {
		AppenderKernel,
		AppenderRunnerReportAgentRole, AppenderRunnerReportGateRole, AppenderRunnerReportExternalAction,
	},
	TypeVerdictRecorded: {AppenderRunnerReportGateRole},
	TypeWarningRaised:   {AppenderRunnerReportGateRole},
	TypeMergePerformed:  {AppenderRunnerReportExternalAction},
	TypeCheckIngested:   {AppenderRunnerReportExternalAction},
	TypeTrunkUnattested: {AppenderRunnerReportExternalAction},
	// The runner switches a model role to its fallback in the Report of
	// the agent run that the provider's limit stopped, and a person
	// switches it back (decisions 0246 and 0266).
	TypeModelRoleSwitched: {AppenderRunnerReportAgentRole, AppenderPerson},

	TypeDecisionMade:  {AppenderControlPlane},
	TypeLeaseGranted:  {AppenderControlPlane},
	TypeLeaseReleased: {AppenderControlPlane},
	TypeLeaseExpired:  {AppenderControlPlane},
}

// Appenders returns the appenders that may append events of typ, sorted.
func Appenders(typ string) []Appender {
	return slices.Sorted(slices.Values(appendTable[typ]))
}

// MayAppend reports whether a may append events of typ.
func MayAppend(typ string, a Appender) bool {
	return slices.Contains(appendTable[typ], a)
}
