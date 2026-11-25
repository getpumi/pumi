// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway

import harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"

// PricesVersion is the version of the list-price table (decision 0229). A
// change to a price changes it, by pull request.
const PricesVersion = "2026-11-25"

// API is a provider's API that the gateway passes through (decisions 0036
// and 0243). Each model in the price table uses one.
type API string

const (
	// MessagesAPI is Anthropic's Messages API, POST /v1/messages.
	MessagesAPI API = "messages"
	// ResponsesAPI is OpenAI's Responses API, POST /v1/responses.
	ResponsesAPI API = "responses"
)

// Rates are list prices in microdollars per million tokens, so that a
// number of tokens times a rate is a cost in picodollars, without rounding.
type Rates struct {
	// Input is the price of input tokens that are not read from or written
	// to the prompt cache.
	Input int64
	// CacheWriteShort is the price of tokens written to the cache for its
	// shorter lifetime: 5 minutes at Anthropic, 30 minutes at OpenAI.
	CacheWriteShort int64
	// CacheWriteLong is the price of tokens written to the cache for its
	// longer lifetime: 1 hour at Anthropic, 12 hours at OpenAI.
	CacheWriteLong int64
	// CacheRead is the price of tokens read from the cache.
	CacheRead int64
	// Output is the price of output tokens, thinking included.
	Output int64
}

// Model is the list prices of one model.
type Model struct {
	// API is the API that the model's calls use.
	API API
	// Standard are the rates of a call at standard speed.
	Standard Rates
	// Fast, when set, are the rates of a call in fast mode: Anthropic's
	// speed "fast", and OpenAI's service tier "priority" or "fast".
	Fast *Rates
	// Flex and Ultrafast, when set, are the rates of OpenAI's service
	// tiers of those names.
	Flex, Ultrafast *Rates
	// LongPrompt, when set, are the rates of a call whose prompt, all its
	// input tokens with the cache reads and writes, is over
	// LongPromptTokens.
	LongPrompt       *Rates
	LongPromptTokens int64
	// LongContextTokens, when set, is the prompt over which OpenAI prices
	// the whole call at its long-context rates: the input and cache rates
	// of the call's tier doubled and its output rate 1.5 times.
	LongContextTokens int64
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
func rates(input, writeShort, writeLong, read, output int64) Rates {
	return Rates{Input: input, CacheWriteShort: writeShort, CacheWriteLong: writeLong, CacheRead: read, Output: output}
}

func ratesPtr(r Rates) *Rates { return &r }

// The list prices of Anthropic's Claude API, from
// https://platform.claude.com/docs/en/about-claude/pricing as read on
// 2026-11-24, in microdollars per million tokens. The models are those the
// page lists that the Claude API still serves, by model ID and, for the
// models before the 4.6 generation, also by dated ID.
var (
	fable52 = Model{
		API:      MessagesAPI,
		Standard: rates(8_000_000, 10_000_000, 16_000_000, 200_000, 40_000_000),
		Fast:     ratesPtr(rates(16_000_000, 20_000_000, 32_000_000, 400_000, 80_000_000)),
	}
	fable51 = Model{API: MessagesAPI, Standard: rates(10_000_000, 12_500_000, 20_000_000, 250_000, 50_000_000)}
	fable5  = Model{API: MessagesAPI, Standard: rates(10_000_000, 12_500_000, 20_000_000, 1_000_000, 50_000_000)}
	opus55  = Model{
		API:      MessagesAPI,
		Standard: rates(4_000_000, 5_000_000, 8_000_000, 200_000, 20_000_000),
		Fast:     ratesPtr(rates(8_000_000, 10_000_000, 16_000_000, 400_000, 40_000_000)),
	}
	opus5 = Model{
		API:      MessagesAPI,
		Standard: rates(5_000_000, 6_250_000, 10_000_000, 500_000, 25_000_000),
		Fast:     ratesPtr(rates(10_000_000, 12_500_000, 20_000_000, 1_000_000, 50_000_000)),
	}
	opus4    = Model{API: MessagesAPI, Standard: rates(5_000_000, 6_250_000, 10_000_000, 500_000, 25_000_000)}
	sonnet55 = Model{API: MessagesAPI, Standard: rates(2_000_000, 2_500_000, 4_000_000, 100_000, 10_000_000)}
	sonnet5  = Model{API: MessagesAPI, Standard: rates(2_000_000, 2_500_000, 4_000_000, 200_000, 10_000_000)}
	sonnet4  = Model{API: MessagesAPI, Standard: rates(3_000_000, 3_750_000, 6_000_000, 300_000, 15_000_000)}
	haiku55  = Model{
		API:              MessagesAPI,
		Standard:         rates(100_000, 125_000, 200_000, 10_000, 500_000),
		LongPrompt:       ratesPtr(rates(500_000, 625_000, 1_000_000, 50_000, 2_500_000)),
		LongPromptTokens: 100_000,
	}
	haiku45 = Model{API: MessagesAPI, Standard: rates(1_000_000, 1_250_000, 2_000_000, 100_000, 5_000_000)}
)

// The list prices of OpenAI's API, from
// https://developers.openai.com/api/docs/pricing as read on 2026-11-25, in
// microdollars per million tokens: input, 30-minute cache writes, 12-hour
// cache writes, cached input and output, at the Standard, Fast, Flex and
// Ultrafast service tiers that each model has. The long-context rule over
// 272,000 input tokens is from the models' pages, such as
// https://developers.openai.com/api/docs/models/gpt-6.2-sol, as read on
// the same day. The models are those the page lists with their Standard
// prices. The Ultrafast prices of gpt-6.2-astra and gpt-6.2-sol are those
// through February 28, 2027, 3 times Standard; from March 1, 2027 they
// are 6 times Standard.
var (
	gpt62Astra = Model{
		API:               ResponsesAPI,
		Standard:          rates(8_000_000, 10_000_000, 20_000_000, 400_000, 40_000_000),
		Fast:              ratesPtr(rates(16_000_000, 20_000_000, 40_000_000, 800_000, 80_000_000)),
		Ultrafast:         ratesPtr(rates(24_000_000, 30_000_000, 60_000_000, 1_200_000, 120_000_000)),
		LongContextTokens: openAILongContext,
	}
	gpt62Sol = Model{
		API:               ResponsesAPI,
		Standard:          rates(2_000_000, 2_500_000, 5_000_000, 100_000, 10_000_000),
		Fast:              ratesPtr(rates(4_000_000, 5_000_000, 10_000_000, 200_000, 20_000_000)),
		Ultrafast:         ratesPtr(rates(6_000_000, 7_500_000, 15_000_000, 300_000, 30_000_000)),
		LongContextTokens: openAILongContext,
	}
	gpt6Astra = Model{
		API:               ResponsesAPI,
		Standard:          rates(10_000_000, 12_500_000, 25_000_000, 1_000_000, 50_000_000),
		Fast:              ratesPtr(rates(20_000_000, 25_000_000, 50_000_000, 2_000_000, 100_000_000)),
		Flex:              ratesPtr(rates(5_000_000, 6_250_000, 12_500_000, 500_000, 25_000_000)),
		Ultrafast:         ratesPtr(rates(60_000_000, 75_000_000, 150_000_000, 6_000_000, 300_000_000)),
		LongContextTokens: openAILongContext,
	}
	gpt61Sol = Model{
		API:               ResponsesAPI,
		Standard:          rates(2_000_000, 2_500_000, 5_000_000, 100_000, 10_000_000),
		Fast:              ratesPtr(rates(4_000_000, 5_000_000, 10_000_000, 200_000, 20_000_000)),
		Flex:              ratesPtr(rates(1_000_000, 1_250_000, 2_500_000, 50_000, 5_000_000)),
		Ultrafast:         ratesPtr(rates(12_000_000, 15_000_000, 30_000_000, 600_000, 60_000_000)),
		LongContextTokens: openAILongContext,
	}
)

// openAILongContext is the prompt, in input tokens, over which OpenAI's
// models are priced at their long-context rates.
const openAILongContext = 272_000

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
	"gpt-6.2-astra":              gpt62Astra,
	"gpt-6.2-sol":                gpt62Sol,
	"gpt-6-astra":                gpt6Astra,
	"gpt-6.1-sol":                gpt61Sol,
}

// Price returns the list prices of the model, and whether the table lists
// it.
func Price(model string) (Model, bool) {
	m, ok := prices[model]
	return m, ok
}

// FigureCost returns the reference cost in picodollars of a usage figure
// that an adapter passes on the sign-in path, at the list prices, as the
// gateway prices the same call (decisions 0229, 0239 and 0241), and
// whether the table lists its model; a figure of a model it does not list
// costs nothing.
func FigureCost(u *harnessv1.UsageFigure) (int64, bool) {
	m, ok := Price(u.GetModel())
	if !ok {
		return 0, false
	}
	return m.picodollars(usage{
		model: u.GetModel(), input: u.GetInputTokens(), writeShort: u.GetFiveMinuteCacheWriteTokens(),
		writeLong: u.GetOneHourCacheWriteTokens(), read: u.GetCacheReadTokens(), output: u.GetOutputTokens(),
		webSearches: u.GetWebSearches(), fast: u.GetSpeed() == "fast", usOnly: u.GetInferenceGeo() == "us",
	}), true
}
