// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway

// PricesVersion is the version of the list-price table (decision 0229). A
// change to a price changes it, by pull request.
const PricesVersion = "2026-11-24"

// Rates are list prices in microdollars per million tokens, so that a
// number of tokens times a rate is a cost in picodollars, without rounding.
type Rates struct {
	// Input is the price of input tokens that are not read from or written
	// to the prompt cache.
	Input int64
	// CacheWrite5m is the price of tokens written to the 5-minute cache.
	CacheWrite5m int64
	// CacheWrite1h is the price of tokens written to the 1-hour cache.
	CacheWrite1h int64
	// CacheRead is the price of tokens read from the cache.
	CacheRead int64
	// Output is the price of output tokens, thinking included.
	Output int64
}

// Model is the list prices of one model.
type Model struct {
	// Standard are the rates of a call at standard speed.
	Standard Rates
	// Fast, when set, are the rates of a call in fast mode.
	Fast *Rates
	// LongPrompt, when set, are the rates of a call whose prompt, all its
	// input tokens with the cache reads and writes, is over
	// LongPromptTokens.
	LongPrompt       *Rates
	LongPromptTokens int64
}

const (
	// webSearchMicrodollars is the price of one web search ($10 for 1,000).
	webSearchMicrodollars = 10_000
	// usOnlyNumerator and usOnlyDenominator are the multiplier of every
	// token price of a call with US-only inference (inference_geo "us"),
	// 1.1.
	usOnlyNumerator   = 11
	usOnlyDenominator = 10
)

// rates builds Rates from prices in microdollars per million tokens.
func rates(input, write5m, write1h, read, output int64) Rates {
	return Rates{Input: input, CacheWrite5m: write5m, CacheWrite1h: write1h, CacheRead: read, Output: output}
}

func ratesPtr(r Rates) *Rates { return &r }

// The list prices of Anthropic's Claude API, from
// https://platform.claude.com/docs/en/about-claude/pricing as read on
// 2026-11-24, in microdollars per million tokens. The models are those the
// page lists that the Claude API still serves, by model ID and, for the
// models before the 4.6 generation, also by dated ID.
var (
	fable52 = Model{
		Standard: rates(8_000_000, 10_000_000, 16_000_000, 200_000, 40_000_000),
		Fast:     ratesPtr(rates(16_000_000, 20_000_000, 32_000_000, 400_000, 80_000_000)),
	}
	fable51 = Model{Standard: rates(10_000_000, 12_500_000, 20_000_000, 250_000, 50_000_000)}
	fable5  = Model{Standard: rates(10_000_000, 12_500_000, 20_000_000, 1_000_000, 50_000_000)}
	opus55  = Model{
		Standard: rates(4_000_000, 5_000_000, 8_000_000, 200_000, 20_000_000),
		Fast:     ratesPtr(rates(8_000_000, 10_000_000, 16_000_000, 400_000, 40_000_000)),
	}
	opus5 = Model{
		Standard: rates(5_000_000, 6_250_000, 10_000_000, 500_000, 25_000_000),
		Fast:     ratesPtr(rates(10_000_000, 12_500_000, 20_000_000, 1_000_000, 50_000_000)),
	}
	opus4    = Model{Standard: rates(5_000_000, 6_250_000, 10_000_000, 500_000, 25_000_000)}
	sonnet55 = Model{Standard: rates(2_000_000, 2_500_000, 4_000_000, 100_000, 10_000_000)}
	sonnet5  = Model{Standard: rates(2_000_000, 2_500_000, 4_000_000, 200_000, 10_000_000)}
	sonnet4  = Model{Standard: rates(3_000_000, 3_750_000, 6_000_000, 300_000, 15_000_000)}
	haiku55  = Model{
		Standard:         rates(100_000, 125_000, 200_000, 10_000, 500_000),
		LongPrompt:       ratesPtr(rates(500_000, 625_000, 1_000_000, 50_000, 2_500_000)),
		LongPromptTokens: 100_000,
	}
	haiku45 = Model{Standard: rates(1_000_000, 1_250_000, 2_000_000, 100_000, 5_000_000)}
)

// prices is the list-price table, by the model name a call gives.
var prices = map[string]Model{
	"claude-fable-5-2":           fable52,
	"claude-mythos-5-2":          fable52,
	"claude-fable-5-1":           fable51,
	"claude-mythos-5-1":          fable51,
	"claude-fable-5":             fable5,
	"claude-mythos-5":            fable5,
	"claude-opus-5-5":            opus55,
	"claude-opus-5":              opus5,
	"claude-opus-4-8":            opus5,
	"claude-opus-4-7":            opus4,
	"claude-opus-4-6":            opus4,
	"claude-opus-4-5":            opus4,
	"claude-opus-4-5-20251101":   opus4,
	"claude-sonnet-5-5":          sonnet55,
	"claude-sonnet-5":            sonnet5,
	"claude-sonnet-4-6":          sonnet4,
	"claude-sonnet-4-5":          sonnet4,
	"claude-sonnet-4-5-20250929": sonnet4,
	"claude-haiku-5-5":           haiku55,
	"claude-haiku-4-5":           haiku45,
	"claude-haiku-4-5-20251001":  haiku45,
}

// Price returns the list prices of the model, and whether the table lists
// it.
func Price(model string) (Model, bool) {
	m, ok := prices[model]
	return m, ok
}
