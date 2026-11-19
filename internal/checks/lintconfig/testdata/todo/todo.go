// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package todo holds a function with a TODO comment, which godox reports
// (decision 0092).
package todo

// Value returns 1.
func Value() int {
	// TODO: return something else.
	return 1
}
