// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

// Package complexity15 holds a function whose cognitive complexity is 15,
// the most that gocognit allows with min-complexity 15.
package complexity15

// Count returns how many of its arguments are true. Each of its 15 if
// statements adds 1 to its cognitive complexity, and nothing else adds to it.
func Count(a, b, c, d, e, f, g, h, i, j, k, l, m, n, o bool) int {
	total := 0
	if a {
		total++
	}
	if b {
		total++
	}
	if c {
		total++
	}
	if d {
		total++
	}
	if e {
		total++
	}
	if f {
		total++
	}
	if g {
		total++
	}
	if h {
		total++
	}
	if i {
		total++
	}
	if j {
		total++
	}
	if k {
		total++
	}
	if l {
		total++
	}
	if m {
		total++
	}
	if n {
		total++
	}
	if o {
		total++
	}
	return total
}
