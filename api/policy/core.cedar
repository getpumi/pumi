// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors
// The core policies of the workflow actions (decisions 0169 to 0171). The
// control plane adds them, with the core invariants, to every policy set.
// The permits let the kernel move a plan with the five workflow actions,
// and invariant.caps still forbids retry, advance and resume over the hard
// caps. The forbid policies hold the retry limit and the rule for ask, so
// that no lower policy set can loosen them.
// The kernel escalates a plan to a person. escalate reads no facts, so it
// is the transition that is always allowed (decision 0169).
@id("core.escalate")
permit (
  principal is Pumi::Kernel,
  action == Pumi::Action::"escalate",
  resource
);

// The kernel cancels a plan on cancel.requested, whose cancel the control
// plane already allowed for the person who asked when it was appended.
// cancel reads no facts, so it is always allowed as well (decision 0177).
@id("core.cancel")
permit (
  principal is Pumi::Kernel,
  action == Pumi::Action::"cancel",
  resource
);

// The kernel retries a gate, advances a plan to the next stage, resumes it
// after a person answered, and asks for a second opinion.
@id("core.workflow-actions")
permit (
  principal is Pumi::Kernel,
  action in
    [Pumi::Action::"retry",
     Pumi::Action::"advance",
     Pumi::Action::"resume",
     Pumi::Action::"ask"],
  resource
);

// A gate is retried only while it has attempts left: its attempts are fewer
// than the profile's maxAttempts for it (decisions 0044 and 0169).
@id("core.attempts-left")
forbid (
  principal,
  action == Pumi::Action::"retry",
  resource
)
unless { context.attempts < context.maxAttempts };

// A plan goes to a second opinion only when its planning output has open
// questions (decision 0171).
@id("core.questions-open")
forbid (
  principal,
  action == Pumi::Action::"ask",
  resource
)
unless { context.questionsOpen > 0 };
