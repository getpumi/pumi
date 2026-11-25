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
	// Upstream is the provider's base URL, such as
	// https://api.anthropic.com.
	Upstream *url.URL
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
// 0233). Both must be in the price table, so both use Anthropic's Messages
// API, and they must differ. A role whose fallback is on another adapter
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
	// adapter is the name of the adapter that runs the invocation.
	adapter string
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
// pass-through proxy of Anthropic's Messages API that counts the tokens and
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
	if cfg.Upstream == nil || cfg.Upstream.Scheme == "" || cfg.Upstream.Host == "" {
		return nil, errors.New("gateway: the provider's URL is not set")
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
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open[invocation] {
		return "", fmt.Errorf("gateway: the invocation %s is already open", invocation)
	}
	g.open[invocation] = true
	g.sessions[token] = &session{
		invocation:        invocation,
		adapter:           adapter,
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
}

type callKey struct{}

// ServeHTTP passes a call of a valid token through to the provider. A
// call of the Messages API is counted: its model must be in the price
// table, and its invocation must not have reached a cap.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s := g.callSession(r)
	if s == nil {
		g.log.Info("gateway refused a call with no valid invocation token", "path", r.URL.Path)
		refuse(w, http.StatusUnauthorized, "authentication_error", "the call has no valid Pumi invocation token")
		return
	}
	if r.URL.Path == "/v1/messages/batches" || strings.HasPrefix(r.URL.Path, "/v1/messages/batches/") {
		g.log.Info("gateway refused a batch, which it cannot count", "invocation", s.invocation, "path", r.URL.Path)
		refuse(w, http.StatusNotFound, "not_found_error", "the Pumi model gateway does not pass batches through, because it cannot count them")
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		g.proxy.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequest))
	if err != nil {
		g.log.Info("gateway could not read a call", "invocation", s.invocation, "error", err.Error())
		refuse(w, http.StatusRequestEntityTooLarge, "request_too_large", "the Pumi model gateway could not read the call")
		return
	}
	c := &call{session: s, body: body}
	plain, readable := decodeBody(body, r.Header.Get("Content-Encoding"))
	named := requestModel(plain, readable)
	c.model = named
	if role, ok := g.roleOf(s, named); ok && readable {
		// The call names a model role: it goes to the role's model, with the
		// body sent uncompressed (decision 0233).
		sent, err := withModel(plain, role.Model)
		if err != nil {
			g.log.Info("gateway could not set the model of a call", "invocation", s.invocation, "error", err.Error())
			refuse(w, http.StatusBadRequest, "invalid_request_error", "the Pumi model gateway could not set the model of the call")
			return
		}
		c.model, c.role, c.fallback, c.body = role.Model, named, role.Fallback, sent
		r.Header.Del("Content-Encoding")
	}
	r.Body = io.NopCloser(bytes.NewReader(c.body))
	r.ContentLength = int64(len(c.body))
	if _, ok := Price(c.model); !ok {
		g.log.Info("gateway refused a call to a model the price table does not list", "invocation", s.invocation, "model", c.model, "prices", PricesVersion)
		refuse(w, http.StatusBadRequest, "invalid_request_error", fmt.Sprintf("the Pumi model gateway's price table (version %s) does not list the model %q", PricesVersion, c.model))
		return
	}
	if g.refuseAtCap(s) {
		g.log.Info("gateway refused a call at the invocation's cap", "invocation", s.invocation)
		refuse(w, http.StatusForbidden, "permission_error", capMessage)
		return
	}
	g.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callKey{}, c)))
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
	if !ok {
		m, _ = Price(c.model)
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

// requestModel reads the model that the uncompressed body of a call of
// the Messages API names. It returns "" when the body cannot be read.
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

// rewrite sends a call to the provider with the credential: the API key in
// place of any the harness sent, or the harness's own. The invocation
// token never reaches the provider. The gateway asks for an uncompressed
// response, so that it can read the usage, and Go's transport then
// compresses the transfer itself.
func (g *Gateway) rewrite(pr *httputil.ProxyRequest) {
	pr.SetURL(g.cfg.Upstream)
	pr.Out.Host = g.cfg.Upstream.Host
	h := pr.Out.Header
	h.Del(InvocationHeader)
	h.Del("Accept-Encoding")
	if s := g.callSession(pr.In); s != nil && s.key != "" {
		h.Del("Authorization")
		h.Set("X-Api-Key", s.key)
	}
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
		reader: newUsageReader(streaming),
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
	refuse(w, http.StatusBadGateway, "api_error", "the Pumi model gateway could not reach the provider")
}

// refuse writes an error in the form of Anthropic's API, with
// x-should-retry false, so that the harness's client does not send the
// call again.
func refuse(w http.ResponseWriter, status int, kind, message string) {
	b, err := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": kind, "message": message},
	})
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
