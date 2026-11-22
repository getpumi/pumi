// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package firstworkflow holds the first workflow, from spec to merge
// (decision 0171), and the text of each task it names. The workflow is
// built into the release: the files are embedded in the binary and pinned
// with the kernel version (decisions 0166 and 0167).
package firstworkflow

import (
	"embed"

	"getpumi.dev/pumi/internal/kernel/workflow"
)

// File is the name of the workflow file among the embedded files.
const File = "workflow.txtpb"

// files are the workflow file and the task texts beside it.
//
//go:embed workflow.txtpb *.md
var files embed.FS

// Load loads the first workflow with the workflow loader, which reads the
// task texts beside it and refuses a workflow that breaks a rule of
// decision 0168 or 0169.
func Load() (*workflow.Workflow, error) {
	return workflow.Load(files, File)
}
