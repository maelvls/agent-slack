package composecmd

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A session is one run of the editor: a loopback HTTP server that serves
// the editor page to the user's browser and takes its "send" or "cancel".
//
// It is the human-in-the-loop boundary of safe mode, so it must only obey
// the page it served to the human's browser:
//
//   - it listens on 127.0.0.1 only, on an ephemeral port;
//   - the page is served once, in exchange for a one-time key carried by
//     the URL given to the browser (a cookie then authorises reloads); a
//     key that leaked afterwards — process list, browser history — is
//     worthless;
//   - the page is handed a session token, required (compared in constant
//     time) on every POST;
//   - requests whose Host is not this server's loopback address (DNS
//     rebinding) or whose Origin / Sec-Fetch-Site says they come from
//     another site are refused, and the JSON Content-Type that is required
//     cannot be sent cross-origin without a preflight, which is never
//     granted.
//
// Lifecycle: the session ends when the page sends or cancels, when the
// context is cancelled (Ctrl-C), when the page is closed (it says goodbye,
// and otherwise stops pinging), or when nobody opens it. A send in flight
// always runs to completion first, and a message is sent at most once.
type session struct {
	page     pageConfig
	send     sendFunc
	timeouts timeouts

	listener net.Listener
	server   *http.Server
	hosts    []string // acceptable Host headers
	cookie   string   // cookie name
	// sendCtx outlives a cancelled command context: a message being
	// posted is not abandoned half-way.
	sendCtx context.Context

	// Secrets, each from crypto/rand.
	key          string // one-time, in the URL
	token        string // for POSTs, known to the page only
	cookieSecret string // lets the browser that used the key reload

	served   chan struct{} // closed when the server goroutine returns
	serveErr error

	mu       sync.Mutex
	state    sessionState
	keyUsed  bool
	timer    *time.Timer
	onExpiry endReason     // why the session ends if timer fires
	deadline time.Time     // when timer is due
	inFlight chan struct{} // closed when the send in progress returns
	result   outcome
	done     chan struct{} // closed when state becomes stateDone
}

// sendFunc posts the reviewed text.
type sendFunc func(ctx context.Context, text string) (sentMessage, error)

// sentMessage describes a posted message.
type sentMessage struct {
	ChannelID string
	TS        string
	ThreadTS  string
	Permalink string
}

type sessionState int

const (
	stateOpen sessionState = iota
	stateSending
	stateDone
)

// endReason says why a session ended without a message being sent.
type endReason string

const (
	endCancelled   endReason = "cancelled in the editor"
	endTabClosed   endReason = "the editor tab was closed (or stopped responding)"
	endNeverOpened endReason = "nobody opened the editor"
	endInterrupted endReason = "interrupted"
)

// outcome is how a session ended.
type outcome struct {
	// Sent is the posted message; nil when nothing was sent.
	Sent *sentMessage
	// Text is the text that was sent, as the human left it.
	Text string
	// Reason is set when nothing was sent.
	Reason endReason
}

// timeouts tune the session's liveness checks.
type timeouts struct {
	// Unopened is how long the page may remain unopened.
	Unopened time.Duration
	// Silent is how long an opened page may go without pinging. Browsers
	// throttle timers of background tabs to as little as once a minute,
	// so this is well above Ping.
	Silent time.Duration
	// Closing is the grace period after the page said it is going away,
	// in which a reload can still bring it back.
	Closing time.Duration
	// Ping is how often the page is asked to ping.
	Ping time.Duration
	// Shutdown bounds the wait for open connections when stopping.
	Shutdown time.Duration
}

var defaultTimeouts = timeouts{
	Unopened: 30 * time.Minute,
	Silent:   150 * time.Second,
	Closing:  5 * time.Second,
	Ping:     10 * time.Second,
	Shutdown: 5 * time.Second,
}

// maxBodyBytes caps a request body: Slack messages are at most 40 000
// characters.
const maxBodyBytes = 1 << 20

// startSession starts serving the editor for page on an ephemeral
// loopback port. The caller must call wait, which also stops the server.
func startSession(ctx context.Context, page pageConfig, send sendFunc, t timeouts) (*session, error) {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting the compose editor on 127.0.0.1: %w", err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	s := &session{
		page:         page,
		send:         send,
		timeouts:     t,
		listener:     listener,
		hosts:        []string{"127.0.0.1:" + port, "localhost:" + port},
		cookie:       "compose_" + port, // cookies are shared across ports
		sendCtx:      context.WithoutCancel(ctx),
		key:          rand.Text(),
		token:        rand.Text(),
		cookieSecret: rand.Text(),
		served:       make(chan struct{}),
		timer:        time.NewTimer(t.Unopened),
		onExpiry:     endNeverOpened,
		deadline:     time.Now().Add(t.Unopened),
		done:         make(chan struct{}),
	}
	s.page.Token = s.token
	s.page.PingMillis = t.Ping.Milliseconds()
	s.server = &http.Server{
		Handler:           http.HandlerFunc(s.serveHTTP),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	go func() {
		defer close(s.served)
		if err := s.server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			s.serveErr = err
		}
	}()
	return s, nil
}

// origin is the editor's address, without any secret.
func (s *session) origin() string { return "http://" + s.hosts[0] }

// url is the address to open in the browser. It carries the one-time key:
// never print it where an agent can read it.
func (s *session) url() string { return s.origin() + "/?k=" + s.key }

// wait blocks until the session ends, then stops the server. It returns an
// error when ctx is cancelled before a message was sent; a send in flight
// at that moment is waited for, and reported as sent if it succeeds.
func (s *session) wait(ctx context.Context) (outcome, error) {
	defer s.stop()
	for {
		select {
		case <-s.done:
			return s.outcome(), nil
		case <-s.timer.C:
			s.expire()
		case <-s.served:
			return outcome{}, fmt.Errorf("the compose editor server stopped unexpectedly: %w", s.serveErr)
		case <-ctx.Done():
			for inFlight := s.interrupt(); inFlight != nil; inFlight = s.interrupt() {
				<-inFlight
			}
			if out := s.outcome(); out.Sent != nil {
				return out, nil
			}
			return outcome{}, fmt.Errorf("compose was interrupted before anything was sent: %w", context.Cause(ctx))
		}
	}
}

// stop shuts the server down, letting responses in flight complete, and
// waits for its goroutine.
func (s *session) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeouts.Shutdown)
	defer cancel()
	if err := s.server.Shutdown(ctx); err != nil {
		_ = s.server.Close()
	}
	<-s.served
	s.mu.Lock()
	s.timer.Stop()
	s.mu.Unlock()
}

func (s *session) outcome() outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result
}

// finishLocked ends the session; s.mu must be held.
func (s *session) finishLocked(result outcome) {
	s.state = stateDone
	s.result = result
	s.timer.Stop()
	close(s.done)
}

// armLocked (re)starts the liveness timer; s.mu must be held. It does
// nothing while a send is in flight or once the session is over.
func (s *session) armLocked(d time.Duration, reason endReason) {
	if s.state != stateOpen {
		return
	}
	s.onExpiry = reason
	s.deadline = time.Now().Add(d)
	s.timer.Reset(d)
}

// expire ends an open session whose liveness timer fired. A firing that
// crossed a sign of life (which re-armed the timer) is ignored.
func (s *session) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateOpen && !time.Now().Before(s.deadline) {
		s.finishLocked(outcome{Reason: s.onExpiry})
	}
}

// interrupt ends an open session. While a send is in flight it returns a
// channel that is closed when that send returns, and changes nothing: the
// caller waits and calls interrupt again.
func (s *session) interrupt() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.state {
	case stateSending:
		return s.inFlight
	case stateOpen:
		s.finishLocked(outcome{Reason: endInterrupted})
	}
	return nil
}

func (s *session) serveHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	// Overridden for the editor page, which needs its script.
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")

	// A page of another site that made its own host name resolve to
	// 127.0.0.1 (DNS rebinding) still sends that name as Host.
	if !s.knownHost(r.Host) {
		http.Error(w, "forbidden: unexpected Host", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/":
		s.servePage(w, r)
	case "/send", "/cancel", "/ping", "/bye":
		s.serveAction(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *session) knownHost(host string) bool {
	for _, h := range s.hosts {
		if host == h {
			return true
		}
	}
	return false
}

func equalSecret(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

const (
	linkSpentMessage = "This editor link is not valid.\n\n" +
		"The link from the command line opens the editor once. If you did not open it yourself, another " +
		"program did: stop the command with Ctrl-C. Otherwise, run the command again.\n"
	sessionOverMessage = "This compose session is over. Run the command again to compose a message.\n"
)

func (s *session) servePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cookie, _ := r.Cookie(s.cookie)
	byCookie := cookie != nil && equalSecret(cookie.Value, s.cookieSecret)
	byKey := equalSecret(r.URL.Query().Get("k"), s.key)

	s.mu.Lock()
	switch {
	case s.state == stateDone:
		s.mu.Unlock()
		http.Error(w, sessionOverMessage, http.StatusGone)
		return
	case byKey && !s.keyUsed:
		s.keyUsed = true
	case byCookie:
	default:
		s.mu.Unlock()
		http.Error(w, linkSpentMessage, http.StatusForbidden)
		return
	}
	s.armLocked(s.timeouts.Silent, endTabClosed)
	s.mu.Unlock()

	nonce := rand.Text()
	page, err := renderPage(s.page, nonce)
	if err != nil {
		http.Error(w, "rendering the editor failed", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookie,
		Value:    s.cookieSecret,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy(nonce))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, page)
}

// actionRequest is the JSON body of every POST.
type actionRequest struct {
	Token string `json:"token"`
	Text  string `json:"text"`
}

type actionResponse struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	TS        string `json:"ts,omitempty"`
	Permalink string `json:"permalink,omitempty"`
}

func reply(w http.ResponseWriter, status int, body actionResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func refuse(w http.ResponseWriter, status int, msg string) {
	reply(w, status, actionResponse{Error: msg})
}

// authorize checks that an action request was made by the editor page and
// returns its body. It has written the response when ok is false.
func (s *session) authorize(w http.ResponseWriter, r *http.Request) (req actionRequest, ok bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		refuse(w, http.StatusMethodNotAllowed, "method not allowed")
		return req, false
	}
	// Browsers put an Origin on every POST, and the page is same-origin.
	// Anything else is another site, or not the page at all.
	if r.Header.Get("Origin") != "http://"+r.Host {
		refuse(w, http.StatusForbidden, "forbidden: cross-origin request")
		return req, false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		refuse(w, http.StatusForbidden, "forbidden: cross-site request")
		return req, false
	}
	// application/json cannot be sent by a form, nor cross-origin without
	// a preflight (which this server never answers).
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		refuse(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return req, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			refuse(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			refuse(w, http.StatusBadRequest, "unreadable request body")
		}
		return req, false
	}
	if err := json.Unmarshal(body, &req); err != nil {
		refuse(w, http.StatusBadRequest, "request body is not valid JSON")
		return req, false
	}
	if !equalSecret(req.Token, s.token) {
		refuse(w, http.StatusForbidden, "forbidden: missing or wrong session token")
		return req, false
	}
	return req, true
}

func (s *session) serveAction(w http.ResponseWriter, r *http.Request) {
	req, ok := s.authorize(w, r)
	if !ok {
		return
	}
	switch r.URL.Path {
	case "/send":
		s.handleSend(w, req.Text)
	case "/cancel":
		s.handleCancel(w)
	case "/ping":
		s.handleLiveness(w, s.timeouts.Silent)
	case "/bye":
		// The tab is closing or reloading: a reload fetches the page
		// again within the grace period.
		s.handleLiveness(w, s.timeouts.Closing)
	}
}

func (s *session) handleLiveness(w http.ResponseWriter, d time.Duration) {
	s.mu.Lock()
	over := s.state == stateDone
	s.armLocked(d, endTabClosed)
	s.mu.Unlock()
	if over {
		refuse(w, http.StatusGone, "this compose session is over")
		return
	}
	reply(w, http.StatusOK, actionResponse{OK: true})
}

func (s *session) handleCancel(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.state == stateSending:
		refuse(w, http.StatusConflict, "the message is being sent: too late to cancel")
		return
	case s.state == stateDone && s.result.Sent != nil:
		refuse(w, http.StatusConflict, "the message was already sent")
		return
	case s.state == stateOpen:
		s.finishLocked(outcome{Reason: endCancelled})
	}
	reply(w, http.StatusOK, actionResponse{OK: true})
}

func (s *session) handleSend(w http.ResponseWriter, text string) {
	if strings.TrimSpace(text) == "" {
		refuse(w, http.StatusBadRequest, "text is required")
		return
	}

	// Only one send may be in flight, and only one may ever succeed: a
	// double click or a replayed request must not post twice.
	s.mu.Lock()
	switch s.state {
	case stateSending:
		s.mu.Unlock()
		refuse(w, http.StatusConflict, "the message is already being sent")
		return
	case stateDone:
		msg := "this compose session is over"
		if s.result.Sent != nil {
			msg = "the message was already sent"
		}
		s.mu.Unlock()
		refuse(w, http.StatusConflict, msg)
		return
	}
	s.state = stateSending
	s.timer.Stop()
	inFlight := make(chan struct{})
	s.inFlight = inFlight
	s.mu.Unlock()

	sent, err := s.send(s.sendCtx, text)

	s.mu.Lock()
	s.inFlight = nil
	if err != nil {
		// The human can fix the message, or simply try again.
		s.state = stateOpen
		s.armLocked(s.timeouts.Silent, endTabClosed)
	} else {
		s.finishLocked(outcome{Sent: &sent, Text: text})
	}
	close(inFlight)
	s.mu.Unlock()

	if err != nil {
		refuse(w, http.StatusBadGateway, redactSecrets(err.Error()))
		return
	}
	reply(w, http.StatusOK, actionResponse{OK: true, TS: sent.TS, Permalink: sent.Permalink})
}

var slackSecretRe = regexp.MustCompile(`xox[a-z]-[A-Za-z0-9%._-]+`)

// redactSecrets removes anything that looks like a Slack token or cookie
// from an error message before it is shown in the browser.
func redactSecrets(msg string) string {
	return slackSecretRe.ReplaceAllString(msg, "[REDACTED]")
}
