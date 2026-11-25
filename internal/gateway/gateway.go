// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: The Pumi Authors

package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	harnessv1 "getpumi.dev/pumi/api/gen/pumi/harness/v1"
)

// InvocationHeader is the header in which a harness sends the invocation
// token on every call. A harness that keeps its own sign-in must send it
// there; when the gateway holds the API key, the harness may instead send
// the token as its API key, in X-Api-Key or as a bearer token (decision
// 0225).
const InvocationHeader = "X-Pumi-Token"

// ErrUnknownToken is returned for a token that the gateway did not open or
// has closed.
var ErrUnknownToken = errors.New("unknown or closed invocation token")

// maxRequest is the largest request body of a model call that the gateway
// reads; Anthropic's API takes at most 32 MB.
const maxRequest = 64 << 20

// Config configures a Gateway.
type Config struct {
	// Upstreams are the providers' base URLs by API, such as
	// https://api.anthropic.com for the Messages API and
	// https://api.openai.com for the Responses API (decision 0243).
	Upstreams map[API]*url.URL
	// AdapterAPIs are the APIs of the adapters' harnesses by adapter name.
	// The calls of an invocation go to the provider of its adapter's API.
	AdapterAPIs map[string]API
	// APIKeys are the adopter's provider API keys by adapter name, for the
	// adapters in the API key mode (decisions 0240 and 0245). The gateway
	// sends the key of an invocation's adapter on every call of the
	// invocation in place of any credential the harness sends. For an
	// adapter with no key, the harness keeps its own sign-in and the
	// gateway forwards the credential the harness sends (decision 0225).
	APIKeys map[string]string
	// Transport sends the calls to the provider; nil is
	// http.DefaultTransport.
	Transport http.RoundTripper
	// Log receives the gateway's refusals and failed calls; nil discards
	// them. The gateway never logs the API key or a token.
	Log *slog.Logger
	// Roles maps each model role, such as "frontier", to its model and
	// fallback model (decision 0233). A call that names a role goes to the
	// role's model; a call that names anything else goes to what it names.
	Roles map[string]Role
	// AdapterRoles maps, by adapter name, the model roles whose fallback is
	// on that adapter and not on the role's own: a call of an invocation of
	// the adapter that names such a role goes to the fallback's model, with
	// no fallback of its own (decision 0246). For the invocations of any
	// other adapter, Roles maps the role.
	AdapterRoles map[string]map[string]Role
	// RetryWait is how long the gateway waits before it sends a call once
	// more after a server error; DefaultRetryWait when zero.
	RetryWait time.Duration
}

// DefaultRetryWait is the wait before the retry of a server error when the
// configuration sets none, the 30 seconds of the regime's MFB-004.
const DefaultRetryWait = 30 * time.Second

// Role is the model and the fallback model of a model role (decision
// 0233). Both must be in the price table, they must use the same API, and
// they must differ. A role whose fallback is on another adapter
// has no fallback model here (decision 0246).
type Role struct {
	// Model is the model that a call naming the role goes to.
	Model string
	// Fallback, when set, is the model that the call goes to once when the
	// provider answers it with a usage or rate limit.
	Fallback string
}

// Totals are an invocation's usage as the gateway counted it (decision
// 0230).
type Totals struct {
	// Tokens are all the tokens of every call: input, output, cache writes
	// and cache reads (decision 0227).
	Tokens int64
	// CostMicrodollars is the reference cost at the list prices of the
	// price table, rounded up to a whole microdollar (decision 0229).
	CostMicrodollars int64
	// CapReached is set when the gateway refused a call of the invocation
	// because its tokens or cost had reached the budget (decision 0228).
	CapReached bool
}

// session is an open invocation.
type session struct {
	invocation string
	// adapter is the name of the adapter that runs the invocation, and api
	// the API of its harness.
	adapter string
	api     API
	// key is the API key of the invocation's adapter, empty when its
	// harness keeps its own sign-in.
	key string
	// budgetTokens and budgetPicodollars are the caps; zero sets none.
	budgetTokens      int64
	budgetPicodollars int64
	// log, when set, is the invocation's log, which receives each fallback
	// and retry.
	log         io.Writer
	tokens      int64
	picodollars int64
	capReached  bool
}

// reached reports whether the session's usage has reached a cap.
func (s *session) reached() bool {
	return (s.budgetTokens > 0 && s.tokens >= s.budgetTokens) ||
		(s.budgetPicodollars > 0 && s.picodollars >= s.budgetPicodollars)
}

// Gateway is the model gateway of embedded mode (decisions 0225 to 0231): a
// pass-through proxy of Anthropic's Messages API and OpenAI's Responses API
// (decision 0243) that counts the tokens and
// the reference cost of each open invocation and refuses its calls once a
// cap is reached. It is an http.Handler.
type Gateway struct {
	cfg   Config
	log   *slog.Logger
	proxy *httputil.ReverseProxy

	// wait waits before a retry; the tests replace it.
	wait func(ctx context.Context, d time.Duration) error

	mu       sync.Mutex
	sessions map[string]*session
	open     map[string]bool
}

// New returns a gateway that sends calls to cfg.Upstream.
func New(cfg Config) (*Gateway, error) {
	if err := checkUpstreams(cfg.Upstreams, cfg.AdapterAPIs); err != nil {
		return nil, err
	}
	if err := checkRoles(cfg.Roles); err != nil {
		return nil, err
	}
	if err := checkAdapterRoles(cfg.AdapterRoles); err != nil {
		return nil, err
	}
	if cfg.RetryWait == 0 {
		cfg.RetryWait = DefaultRetryWait
	}
	transport := cfg.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	g := &Gateway{
		cfg:      cfg,
		wait:     sleep,
		log:      cfg.Log,
		sessions: map[string]*session{},
		open:     map[string]bool{},
	}
	if g.log == nil {
		g.log = slog.New(slog.DiscardHandler)
	}
	g.proxy = &httputil.ReverseProxy{
		Rewrite:        g.rewrite,
		Transport:      &attempts{g: g, base: transport},
		FlushInterval:  -1,
		ModifyResponse: g.modifyResponse,
		ErrorHandler:   g.proxyError,
		ErrorLog:       slog.NewLogLogger(g.log.Handler(), slog.LevelWarn),
	}
	return g, nil
}

// checkUpstreams refuses a gateway with no provider, a provider's URL
// that is not set, and an adapter whose API the gateway does not serve or
// has no provider for.
func checkUpstreams(upstreams map[API]*url.URL, adapters map[string]API) error {
	if len(upstreams) == 0 {
		return errors.New("gateway: no provider's URL is set")
	}
	var errs []error
	for api, u := range upstreams {
		if api != MessagesAPI && api != ResponsesAPI {
			errs = append(errs, fmt.Errorf("gateway: the gateway does not serve the API %q", api))
		}
		if u == nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("gateway: the URL of the provider of the %s API is not set", api))
		}
	}
	for adapter, api := range adapters {
		if _, ok := upstreams[api]; !ok {
			errs = append(errs, fmt.Errorf("gateway: the adapter %q uses the %s API, for which no provider's URL is set", adapter, api))
		}
	}
	return errors.Join(errs...)
}

// KeepsSignIn reports whether the harness of the adapter, by name, keeps
// its own sign-in, which is when the configuration has no API key for the
// adapter. The harness then sends the token in InvocationHeader (decisions
// 0225 and 0240).
func (g *Gateway) KeepsSignIn(adapter string) bool { return g.cfg.APIKeys[adapter] == "" }

// Open opens an invocation, which the adapter runs, with its budget and
// returns a random token
// that is valid for it until Close (decision 0231). A budget's zero tokens
// or zero cost sets no cap of that kind; its seconds are the runner's.
// The gateway writes each fallback and retry of the invocation's calls to
// log, the invocation's log, when it is not nil (decision 0233).
func (g *Gateway) Open(invocation, adapter string, budget *harnessv1.Usage, log io.Writer) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("gateway: making a token: %w", err)
	}
	token := hex.EncodeToString(b)
	api, ok := g.cfg.AdapterAPIs[adapter]
	if !ok {
		return "", fmt.Errorf("gateway: the adapter %q has no API", adapter)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open[invocation] {
		return "", fmt.Errorf("gateway: the invocation %s is already open", invocation)
	}
	g.open[invocation] = true
	g.sessions[token] = &session{
		invocation:        invocation,
		adapter:           adapter,
		api:               api,
		key:               g.cfg.APIKeys[adapter],
		budgetTokens:      budget.GetTokens(),
		budgetPicodollars: budget.GetCostMicrodollars() * 1_000_000,
		log:               log,
	}
	return token, nil
}

// Totals returns the totals of the invocation the token was opened for.
func (g *Gateway) Totals(token string) (Totals, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.sessions[token]
	if !ok {
		return Totals{}, ErrUnknownToken
	}
	return Totals{
		Tokens:           s.tokens,
		CostMicrodollars: (s.picodollars + 999_999) / 1_000_000,
		CapReached:       s.capReached,
	}, nil
}

// Close ends the token. A call still in progress finishes, but its usage
// is no longer reported.
func (g *Gateway) Close(token string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	s, ok := g.sessions[token]
	if !ok {
		return ErrUnknownToken
	}
	delete(g.sessions, token)
	delete(g.open, s.invocation)
	return nil
}

// roleOf returns the model role named of the session's invocation: from
// AdapterRoles when its adapter is the fallback of the role, and from
// Roles otherwise (decision 0246).
func (g *Gateway) roleOf(s *session, named string) (Role, bool) {
	if r, ok := g.cfg.AdapterRoles[s.adapter][named]; ok {
		return r, true
	}
	r, ok := g.cfg.Roles[named]
	return r, ok
}

// call is what ServeHTTP passes to the proxy about a counted call.
type call struct {
	session *session
	// model is the model the call goes to now.
	model string
	// role is the model role the call named, or empty when it named a
	// model; fallback is the role's fallback model.
	role, fallback string
	// body is the body sent to the provider, uncompressed when the call
	// named a role.
	body []byte
	// longCache is a call of the Responses API that asks for 12-hour cache
	// writes, whose writes are all priced at the 12-hour rate.
	longCache bool
}

type callKey struct{}

// ServeHTTP passes a call of a valid token through to the provider of its
// invocation's API. A call of the Messages API (POST /v1/messages) or the
// Responses API (POST /v1/responses) is counted: its model must be in the
// price table for that API, and its invocation must not have reached a
// cap.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s := g.callSession(r)
	if s == nil {
		g.log.Info("gateway refused a call with no valid invocation token", "path", r.URL.Path)
		api := MessagesAPI
		if r.URL.Path == "/v1/responses" || strings.HasPrefix(r.URL.Path, "/v1/responses/") {
			api = ResponsesAPI
		}
		refuse(w, api, http.StatusUnauthorized, "authentication_error", "the call has no valid Pumi invocation token")
		return
	}
	counted, ok := g.route(w, r, s)
	if !ok {
		return
	}
	if !counted {
		g.proxy.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequest))
	if err != nil {
		g.log.Info("gateway could not read a call", "invocation", s.invocation, "error", err.Error())
		refuse(w, s.api, http.StatusRequestEntityTooLarge, "request_too_large", "the Pumi model gateway could not read the call")
		return
	}
	c := g.newCall(w, r, s, body)
	if c == nil {
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(c.body))
	r.ContentLength = int64(len(c.body))
	if m, ok := Price(c.model); !ok || m.API != s.api {
		g.log.Info("gateway refused a call to a model the price table does not list for its API", "invocation", s.invocation, "model", c.model, "api", string(s.api), "prices", PricesVersion)
		refuse(w, s.api, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("the Pumi model gateway's price table (version %s) does not list the model %q for the %s API", PricesVersion, c.model, s.api))
		return
	}
	if g.refuseAtCap(s) {
		g.log.Info("gateway refused a call at the invocation's cap", "invocation", s.invocation)
		refuse(w, s.api, http.StatusForbidden, "permission_error", capMessage)
		return
	}
	g.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callKey{}, c)))
}

// newCall reads a counted call of the session: the model it names, the
// role's model when it names a model role, and for the Responses API the
// cache lifetime it asks for. It refuses a background response, and a
// call whose model it cannot set, and then returns nil.
func (g *Gateway) newCall(w http.ResponseWriter, r *http.Request, s *session, body []byte) *call {
	c := &call{session: s, body: body}
	plain, readable := decodeBody(body, r.Header.Get("Content-Encoding"))
	if s.api == ResponsesAPI && readable {
		if background(plain) {
			g.log.Info("gateway refused a background response, which it cannot count", "invocation", s.invocation)
			refuse(w, s.api, http.StatusBadRequest, "invalid_request_error", "the Pumi model gateway does not pass background responses through, because it cannot count them")
			return nil
		}
		c.longCache = longCacheWrites(plain)
	}
	named := requestModel(plain, readable)
	c.model = named
	role, ok := g.roleOf(s, named)
	if !ok || !readable {
		return c
	}
	// The call names a model role: it goes to the role's model, with the
	// body sent uncompressed (decision 0233).
	sent, err := withModel(plain, role.Model)
	if err != nil {
		g.log.Info("gateway could not set the model of a call", "invocation", s.invocation, "error", err.Error())
		refuse(w, s.api, http.StatusBadRequest, "invalid_request_error", "the Pumi model gateway could not set the model of the call")
		return nil
	}
	c.model, c.role, c.fallback, c.body = role.Model, named, role.Fallback, sent
	r.Header.Del("Content-Encoding")
	return c
}

// route decides what the gateway does with a request of the session: it
// counts the API's calls, passes the requests that cost nothing through
// uncounted and refuses those it cannot count, in which case ok is false.
// For the Messages API it refuses batches and passes every other request
// through. For the Responses API it passes only the model list through.
func (g *Gateway) route(w http.ResponseWriter, r *http.Request, s *session) (counted, ok bool) {
	path := r.URL.Path
	if s.api == MessagesAPI {
		if path == "/v1/messages/batches" || strings.HasPrefix(path, "/v1/messages/batches/") {
			g.log.Info("gateway refused a batch, which it cannot count", "invocation", s.invocation, "path", path)
			refuse(w, s.api, http.StatusNotFound, "not_found_error", "the Pumi model gateway does not pass batches through, because it cannot count them")
			return false, false
		}
		return r.Method == http.MethodPost && path == "/v1/messages", true
	}
	if r.Method == http.MethodPost && path == "/v1/responses" {
		return true, true
	}
	if r.Method == http.MethodGet && (path == "/v1/models" || strings.HasPrefix(path, "/v1/models/")) {
		return false, true
	}
	g.log.Info("gateway refused a request of the Responses API that it does not pass through", "invocation", s.invocation, "method", r.Method, "path", path)
	refuse(w, s.api, http.StatusNotFound, "invalid_request_error", fmt.Sprintf("the Pumi model gateway does not pass %s %s through, because it cannot count it; it passes POST /v1/responses and GET /v1/models", r.Method, path))
	return false, false
}

// background reports whether the uncompressed body of a call of the
// Responses API asks for a background response.
func background(plain []byte) bool {
	var b struct {
		Background bool `json:"background"`
	}
	return json.Unmarshal(plain, &b) == nil && b.Background
}

// capMessage is the message of a call refused at the invocation's cap.
const capMessage = "the invocation has reached its token or cost cap"

// callSession finds the session of a call by its invocation token: in
// InvocationHeader, or, for an invocation whose adapter has an API key, as
// the harness's API key or bearer token. A harness that keeps its own
// sign-in sends its sign-in there, not a token (decision 0225).
func (g *Gateway) callSession(r *http.Request) *session {
	if t := r.Header.Get(InvocationHeader); t != "" {
		return g.session(t)
	}
	t := r.Header.Get("X-Api-Key")
	if t == "" {
		t, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if s := g.session(t); s != nil && s.key != "" {
		return s
	}
	return nil
}

func (g *Gateway) session(token string) *session {
	if token == "" {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessions[token]
}

// refuseAtCap reports whether the session has reached a cap, and records
// that a call was refused for it.
func (g *Gateway) refuseAtCap(s *session) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !s.reached() {
		return false
	}
	s.capReached = true
	return true
}

// record adds a call's usage to its session.
func (g *Gateway) record(c *call, u usage) {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, ok := Price(u.model)
	if !ok || m.API != c.session.api {
		m, _ = Price(c.model)
	}
	if c.longCache {
		u.writeShort, u.writeLong = 0, u.writeShort+u.writeLong
	}
	cost := m.picodollars(u)
	g.log.Debug("gateway counted a call", "invocation", c.session.invocation, "model", c.model, "tokens", u.tokens(), "picodollars", cost)
	c.session.tokens += u.tokens()
	c.session.picodollars += cost
}

// decodeBody returns the uncompressed body of a call, and false when it
// cannot be read.
func decodeBody(body []byte, encoding string) ([]byte, bool) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return body, true
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, false
		}
		plain, err := io.ReadAll(io.LimitReader(zr, maxRequest))
		if err != nil {
			return nil, false
		}
		return plain, true
	default:
		return nil, false
	}
}

// requestModel reads the model that the uncompressed body of a call
// names. It returns "" when the body cannot be read.
func requestModel(plain []byte, readable bool) string {
	if !readable {
		return ""
	}
	var m struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(plain, &m) != nil {
		return ""
	}
	return m.Model
}

// rewrite sends a call to the provider of its invocation's API with the
// credential: the API key in place of any the harness sent, in X-Api-Key
// for Anthropic and as the bearer token for OpenAI, or the harness's own.
// The invocation token never reaches the provider. The gateway asks for an uncompressed
// response, so that it can read the usage, and Go's transport then
// compresses the transfer itself.
func (g *Gateway) rewrite(pr *httputil.ProxyRequest) {
	s := g.callSession(pr.In)
	upstream := g.cfg.Upstreams[s.api]
	pr.SetURL(upstream)
	pr.Out.Host = upstream.Host
	h := pr.Out.Header
	h.Del(InvocationHeader)
	h.Del("Accept-Encoding")
	if s.key == "" {
		return
	}
	if s.api == ResponsesAPI {
		h.Del("X-Api-Key")
		h.Set("Authorization", "Bearer "+s.key)
		return
	}
	h.Del("Authorization")
	h.Set("X-Api-Key", s.key)
}

// modifyResponse counts the usage of a successful counted call as its body
// passes to the harness.
func (g *Gateway) modifyResponse(resp *http.Response) error {
	c, ok := resp.Request.Context().Value(callKey{}).(*call)
	if !ok || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil
	}
	streaming := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	resp.Body = &countedBody{
		body:   resp.Body,
		reader: newUsageReader(c.session.api, streaming),
		done: func(u usage, found, tooLong bool) {
			switch {
			case found:
				g.record(c, u)
			case tooLong:
				g.log.Warn("gateway passed a response through that was too long to count", "invocation", c.session.invocation, "model", c.model)
			default:
				g.log.Warn("gateway found no usage in a response", "invocation", c.session.invocation, "model", c.model)
			}
		},
	}
	return nil
}

// proxyError answers a call that could not reach the provider.
func (g *Gateway) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	g.log.Warn("gateway could not reach the provider", "path", r.URL.Path, "error", err.Error())
	api := MessagesAPI
	if s := g.callSession(r); s != nil {
		api = s.api
	}
	refuse(w, api, http.StatusBadGateway, "api_error", "the Pumi model gateway could not reach the provider")
}

// errorBody is an error in the form of the API: Anthropic's, or OpenAI's,
// with no param or code.
func errorBody(api API, kind, message string) ([]byte, error) {
	if api == ResponsesAPI {
		return json.Marshal(map[string]any{
			"error": map[string]any{"message": message, "type": kind, "param": nil, "code": nil},
		})
	}
	return json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": kind, "message": message},
	})
}

// refuse writes an error in the form of the API, with x-should-retry
// false, so that the harness's client does not send the call again.
// Codex CLI 0.172.0 ignores the header: it sends a call refused with 401,
// 403 or 404 five more times, each refused again, and then fails the turn,
// and a call refused with 400 not again.
func refuse(w http.ResponseWriter, api API, status int, kind, message string) {
	b, err := errorBody(api, kind, message)
	if err != nil {
		http.Error(w, message, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Should-Retry", "false")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// countedBody passes a response body through and gives its usage to done
// once, at the end of the body or when it is closed.
type countedBody struct {
	body   io.ReadCloser
	reader *usageReader
	once   sync.Once
	done   func(u usage, found, tooLong bool)
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	_, _ = b.reader.Write(p[:n])
	if err != nil {
		b.finish()
	}
	return n, err
}

func (b *countedBody) Close() error {
	b.finish()
	return b.body.Close()
}

func (b *countedBody) finish() {
	b.once.Do(func() {
		u, found := b.reader.usage()
		b.done(u, found, b.reader.tooLong)
	})
}
