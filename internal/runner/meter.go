// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package runner

import (
	"fmt"
	"sync"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// picodollarsPerMicrodollar converts a cost in microdollars, as budgets
// and results give it, to the picodollars that the price table gives.
const picodollarsPerMicrodollar = 1_000_000

// meter counts and prices the usage figures that the adapter of an agent
// run on the sign-in path passes, and says when the run must stop: once
// its tokens or cost reach the budget, or when a figure cannot be priced
// (decision 0241). It is safe for concurrent use.
type meter struct {
	price func(*harnessv1.UsageFigure) (int64, bool)
	// budgetTokens and budgetPicodollars are the caps; zero sets none.
	budgetTokens, budgetPicodollars int64

	mu          sync.Mutex
	figures     int
	tokens      int64
	picodollars int64
	// err is why the run cannot be counted, such as a model that the price
	// table does not list.
	err error
	// stop is closed once the run must stop.
	stop    chan struct{}
	stopped bool
}

// newMeter returns a meter for the budget that prices with price.
func newMeter(budget *harnessv1.Usage, price func(*harnessv1.UsageFigure) (int64, bool)) *meter {
	return &meter{
		price:             price,
		budgetTokens:      budget.GetTokens(),
		budgetPicodollars: budget.GetCostMicrodollars() * picodollarsPerMicrodollar,
		stop:              make(chan struct{}),
	}
}

// figureTokens returns the tokens of every kind of the figure, which count
// toward the token cap (decision 0227).
func figureTokens(u *harnessv1.UsageFigure) int64 {
	return u.GetInputTokens() + u.GetFiveMinuteCacheWriteTokens() + u.GetOneHourCacheWriteTokens() + u.GetCacheReadTokens() + u.GetOutputTokens()
}

// add counts one figure.
func (m *meter) add(u *harnessv1.UsageFigure) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.figures++
	cost, listed := m.price(u)
	if !listed && m.err == nil {
		m.err = fmt.Errorf("the list-price table does not list the model %q of a usage figure, so the runner cannot count the run's cost (decision 0241)", u.GetModel())
	}
	m.tokens += figureTokens(u)
	m.picodollars += cost
	if (m.err != nil || m.reached(m.tokens, m.picodollars)) && !m.stopped {
		m.stopped = true
		close(m.stop)
	}
}

// reached reports whether tokens or picodollars reach a cap.
func (m *meter) reached(tokens, picodollars int64) bool {
	return (m.budgetTokens > 0 && tokens >= m.budgetTokens) ||
		(m.budgetPicodollars > 0 && picodollars >= m.budgetPicodollars)
}

// failure returns why the run cannot be counted, or nil.
func (m *meter) failure() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// counted is a run's usage as the runner counted it on the sign-in path.
type counted struct {
	tokens, microdollars int64
	// figures is the number of figures the adapter passed.
	figures int
	// capReached is set when the usage reaches a cap.
	capReached bool
}

// count returns the run's usage: the count of the figures, or, when the
// adapter passed none, the usage of its result res, which may be nil. The
// caps are checked on either (decision 0241).
func (m *meter) count(res *harnessv1.Result) counted {
	m.mu.Lock()
	defer m.mu.Unlock()
	tokens, picodollars := m.tokens, m.picodollars
	if m.figures == 0 {
		tokens = res.GetUsage().GetTokens()
		picodollars = res.GetUsage().GetCostMicrodollars() * picodollarsPerMicrodollar
	}
	return counted{
		tokens:       tokens,
		microdollars: (picodollars + picodollarsPerMicrodollar - 1) / picodollarsPerMicrodollar,
		figures:      m.figures,
		capReached:   m.reached(tokens, picodollars),
	}
}

// current returns the tokens and microdollars counted so far.
func (m *meter) current() (int64, int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens, (m.picodollars + picodollarsPerMicrodollar - 1) / picodollarsPerMicrodollar
}
