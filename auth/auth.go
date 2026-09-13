// Package auth provides the small, server-side authentication primitives used by FileHarbor.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName                = "fileharbor_session"
	LegacyCookieName          = "gofile_session"
	SessionDuration           = 12 * time.Hour
	RememberedSessionDuration = 30 * 24 * time.Hour
	TicketDuration            = time.Minute
	ListingDuration           = 5 * time.Minute

	maxLoginFailures            = 5
	loginCooldown               = time.Minute
	maxTrackedLoginIPs          = 4096
	maxConcurrentPasswordChecks = 4
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrRateLimited        = errors.New("too many login attempts")
	ErrInvalidConfig      = errors.New("invalid authentication configuration")
)

// Config holds the single-administrator credentials and session settings.
type Config struct {
	Username      string
	PasswordHash  string
	SessionSecret string
	APIToken      string
	CookieSecure  bool
	CookiePath    string
}

// ConfigFromLookup loads FileHarbor configuration without validating it. A
// deployment must use either the FILEHARBOR_* namespace or the temporary
// GOFILE_* compatibility namespace, never both. Refusing partial mixed sets
// prevents credentials from being silently assembled from two deployments.
func ConfigFromLookup(lookup func(string) string, cookieSecure bool) (Config, error) {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}

	primaryKeys := []string{
		"FILEHARBOR_ADMIN_USERNAME",
		"FILEHARBOR_ADMIN_PASSWORD_HASH",
		"FILEHARBOR_SESSION_SECRET",
		"FILEHARBOR_API_TOKEN",
	}
	legacyKeys := []string{
		"GOFILE_ADMIN_USERNAME",
		"GOFILE_ADMIN_PASSWORD_HASH",
		"GOFILE_SESSION_SECRET",
		"GOFILE_API_TOKEN",
	}
	primaryConfigured := anyConfigured(lookup, primaryKeys)
	legacyConfigured := anyConfigured(lookup, legacyKeys)
	if primaryConfigured && legacyConfigured {
		return Config{}, ErrInvalidConfig
	}

	keys := primaryKeys
	if legacyConfigured {
		keys = legacyKeys
	}
	return Config{
		Username:      lookup(keys[0]),
		PasswordHash:  lookup(keys[1]),
		SessionSecret: lookup(keys[2]),
		APIToken:      lookup(keys[3]),
		CookieSecure:  cookieSecure,
	}, nil
}

func anyConfigured(lookup func(string) string, keys []string) bool {
	for _, key := range keys {
		if lookup(key) != "" {
			return true
		}
	}
	return false
}

// ConfigFromEnv loads and validates the mandatory single-administrator credentials.
func ConfigFromEnv(cookieSecure bool) (Config, error) {
	cfg, err := ConfigFromLookup(os.Getenv, cookieSecure)
	if err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects missing credentials, malformed password hashes, and
// predictably short server secrets.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Username) == "" || !validPasswordHash(c.PasswordHash) ||
		len(c.SessionSecret) < 32 || len(c.APIToken) < 32 {
		return ErrInvalidConfig
	}
	return nil
}

type session struct {
	Username string
	CSRF     string
	Expires  time.Time
}

type loginAttempt struct {
	Failures    int
	RetryAt     time.Time
	LastAttempt time.Time
	Checking    bool
}

// ListingItem is a direct-child entry rendered into a listing page.
type ListingItem struct {
	Name    string
	Version string
}

type listing struct {
	SessionID string
	Directory string
	Items     map[string]string
	Expires   time.Time
}

// ArchiveItem describes a root-relative item permitted by a download ticket.
type ArchiveItem struct {
	Path    string
	Version string
}

type archiveTicket struct {
	SessionID string
	Items     []ArchiveItem
	Expires   time.Time
}

// Manager keeps short-lived sessions and browser-only operation tickets in memory.
// Restarting FileHarbor deliberately invalidates all of them.
type Manager struct {
	config          Config
	now             func() time.Time
	comparePassword func([]byte, []byte) error // test seam for bcrypt verification
	passwordChecks  chan struct{}
	mu              sync.Mutex
	sessions        map[string]session
	attempts        map[string]loginAttempt
	listings        map[string]listing
	tickets         map[string]archiveTicket
}

// Info is attached to authenticated requests by the middleware.
type Info struct {
	Username  string
	SessionID string
	CSRF      string
	Expires   time.Time
	Bearer    bool
}

func validCookiePath(path string) bool {
	if path == "/" {
		return true
	}
	if !strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") || strings.Contains(path, "//") || strings.ContainsAny(path, "\\;?#%") {
		return false
	}
	for _, r := range path {
		if r > 0x7e || r < 0x20 {
			return false
		}
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func NewManager(config Config) (*Manager, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.CookiePath == "" {
		config.CookiePath = "/"
	}
	if !validCookiePath(config.CookiePath) {
		return nil, ErrInvalidConfig
	}
	return &Manager{
		config:          config,
		now:             time.Now,
		comparePassword: bcrypt.CompareHashAndPassword,
		passwordChecks:  make(chan struct{}, maxConcurrentPasswordChecks),
		sessions:        make(map[string]session),
		attempts:        make(map[string]loginAttempt),
		listings:        make(map[string]listing),
		tickets:         make(map[string]archiveTicket),
	}, nil
}

// Login validates the only configured administrator account and opens a session.
func (m *Manager) Login(ip, username, password string) (Info, string, time.Time, error) {
	return m.LoginWithRemember(ip, username, password, false)
}

// LoginWithRemember changes only the absolute session lifetime, not credential
// verification, throttling, or server-side revocation.
func (m *Manager) LoginWithRemember(ip, username, password string, remember bool) (Info, string, time.Time, error) {
	m.mu.Lock()
	now := m.now()
	m.cleanupLocked(now)
	attempt, tracked := m.attempts[ip]
	if (tracked && now.Before(attempt.RetryAt)) || attempt.Checking {
		m.mu.Unlock()
		return Info{}, "", time.Time{}, ErrRateLimited
	}
	if !tracked && len(m.attempts) >= maxTrackedLoginIPs {
		if !m.evictOldestAttemptLocked() {
			m.mu.Unlock()
			return Info{}, "", time.Time{}, ErrRateLimited
		}
	}
	attempt.Checking = true
	m.attempts[ip] = attempt
	m.mu.Unlock()

	select {
	case m.passwordChecks <- struct{}{}:
		defer func() { <-m.passwordChecks }()
	default:
		m.mu.Lock()
		if attempt, ok := m.attempts[ip]; ok {
			attempt.Checking = false
			m.attempts[ip] = attempt
		}
		m.mu.Unlock()
		return Info{}, "", time.Time{}, ErrRateLimited
	}

	passwordBytes := []byte(password)
	defer ClearPassword(passwordBytes)
	candidatePassword, passwordValid := passwordForComparison(passwordBytes)
	validUser := subtle.ConstantTimeCompare([]byte(username), []byte(m.config.Username)) == 1
	passwordMatches := m.comparePassword([]byte(m.config.PasswordHash), candidatePassword) == nil

	m.mu.Lock()
	defer m.mu.Unlock()
	now = m.now()
	attempt, tracked = m.attempts[ip]
	if !tracked || now.Before(attempt.RetryAt) || !attempt.Checking {
		return Info{}, "", time.Time{}, ErrRateLimited
	}
	attempt.Checking = false
	if !validUser || !passwordValid || !passwordMatches {
		attempt.Failures++
		attempt.LastAttempt = now
		if attempt.Failures >= maxLoginFailures {
			attempt.Failures = 0
			attempt.RetryAt = now.Add(loginCooldown)
		}
		m.attempts[ip] = attempt
		return Info{}, "", time.Time{}, ErrInvalidCredentials
	}
	delete(m.attempts, ip)

	id, err := randomToken(32)
	if err != nil {
		return Info{}, "", time.Time{}, err
	}
	csrf, err := randomToken(32)
	if err != nil {
		return Info{}, "", time.Time{}, err
	}
	duration := SessionDuration
	if remember {
		duration = RememberedSessionDuration
	}
	expires := now.Add(duration)
	m.sessions[id] = session{Username: m.config.Username, CSRF: csrf, Expires: expires}
	return Info{Username: m.config.Username, SessionID: id, CSRF: csrf, Expires: expires}, m.signSessionID(id), expires, nil
}

func (m *Manager) SessionFromRequest(r *http.Request) (Info, bool) {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return Info{}, false
	}
	id, ok := m.verifySessionCookie(cookie.Value)
	if !ok {
		return Info{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.cleanupLocked(now)
	s, ok := m.sessions[id]
	if !ok || !now.Before(s.Expires) {
		return Info{}, false
	}
	return Info{Username: s.Username, SessionID: id, CSRF: s.CSRF, Expires: s.Expires}, true
}

func (m *Manager) IsBearerToken(value string) bool {
	if !strings.HasPrefix(value, "Bearer ") {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(m.config.APIToken)) == 1
}

func (m *Manager) CheckCSRF(info Info, supplied string) bool {
	if info.Bearer {
		return true
	}
	return info.SessionID != "" && supplied != "" && subtle.ConstantTimeCompare([]byte(info.CSRF), []byte(supplied)) == 1
}

func (m *Manager) Logout(info Info) {
	if info.SessionID == "" {
		return
	}
	m.mu.Lock()
	delete(m.sessions, info.SessionID)
	m.mu.Unlock()
}

func (m *Manager) Cookie(value string, expires time.Time) *http.Cookie {
	return m.sessionCookie(CookieName, value, expires, int(SessionDuration.Seconds()))
}

// LoginCookie keeps ordinary logins browser-session-only. Remembered logins
// remain revocable server sessions, never credentials stored in the browser.
func (m *Manager) LoginCookie(value string, expires time.Time, remember bool) *http.Cookie {
	if !remember {
		return m.sessionCookie(CookieName, value, time.Time{}, 0)
	}
	return m.sessionCookie(CookieName, value, expires, int(RememberedSessionDuration.Seconds()))
}

func (m *Manager) ExpiredCookie() *http.Cookie {
	return m.sessionCookie(CookieName, "", time.Time{}, -1)
}

// ExpiredLegacyCookie clears the prior goFile session cookie during migration.
func (m *Manager) ExpiredLegacyCookie() *http.Cookie {
	return m.sessionCookie(LegacyCookieName, "", time.Time{}, -1)
}

func (m *Manager) sessionCookie(name, value string, expires time.Time, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     m.config.CookiePath,
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   m.config.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	}
}

// IssueListing creates a short-lived, session-bound permission envelope for the
// exact direct children rendered in a browser listing.
func (m *Manager) IssueListing(sessionID, directory string, entries []ListingItem) (string, error) {
	if sessionID == "" {
		return "", errors.New("browser session required")
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	items := make(map[string]string, len(entries))
	for _, entry := range entries {
		items[entry.Name] = entry.Version
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.cleanupLocked(now)
	m.listings[token] = listing{SessionID: sessionID, Directory: directory, Items: items, Expires: now.Add(ListingDuration)}
	return token, nil
}

// ConsumeListing verifies a listing token without consuming it. It remains valid
// for short batch-operation retries until expiry.
func (m *Manager) ReadListing(sessionID, token string) (string, map[string]string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.cleanupLocked(now)
	listing, ok := m.listings[token]
	if !ok || listing.SessionID != sessionID || !now.Before(listing.Expires) {
		return "", nil, false
	}
	items := make(map[string]string, len(listing.Items))
	for name, version := range listing.Items {
		items[name] = version
	}
	return listing.Directory, items, true
}

func (m *Manager) IssueArchiveTicket(sessionID string, items []ArchiveItem) (string, error) {
	if sessionID == "" {
		return "", errors.New("browser session required")
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.cleanupLocked(now)
	m.tickets[token] = archiveTicket{SessionID: sessionID, Items: append([]ArchiveItem(nil), items...), Expires: now.Add(TicketDuration)}
	return token, nil
}

// ConsumeArchiveTicket is atomic and makes a ticket unusable after one request.
func (m *Manager) ConsumeArchiveTicket(sessionID, token string) ([]ArchiveItem, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.cleanupLocked(now)
	ticket, ok := m.tickets[token]
	if !ok || ticket.SessionID != sessionID || !now.Before(ticket.Expires) {
		return nil, false
	}
	delete(m.tickets, token)
	return append([]ArchiveItem(nil), ticket.Items...), true
}

func (m *Manager) signSessionID(id string) string {
	mac := hmac.New(sha256.New, []byte(m.config.SessionSecret))
	_, _ = mac.Write([]byte(id))
	return id + "." + hex.EncodeToString(mac.Sum(nil))
}

func (m *Manager) verifySessionCookie(value string) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[0]) != 64 {
		return "", false
	}
	expected := m.signSessionID(parts[0])
	if subtle.ConstantTimeCompare([]byte(expected), []byte(value)) != 1 {
		return "", false
	}
	return parts[0], true
}

func (m *Manager) evictOldestAttemptLocked() bool {
	var oldestIP string
	var oldest time.Time
	for ip, attempt := range m.attempts {
		if attempt.Checking {
			continue
		}
		if oldestIP == "" || attempt.LastAttempt.Before(oldest) {
			oldestIP = ip
			oldest = attempt.LastAttempt
		}
	}
	if oldestIP == "" {
		return false
	}
	delete(m.attempts, oldestIP)
	return true
}

func (m *Manager) cleanupLocked(now time.Time) {
	for id, s := range m.sessions {
		if !now.Before(s.Expires) {
			delete(m.sessions, id)
		}
	}
	for token, listing := range m.listings {
		if !now.Before(listing.Expires) {
			delete(m.listings, token)
		}
	}
	for token, ticket := range m.tickets {
		if !now.Before(ticket.Expires) {
			delete(m.tickets, token)
		}
	}
	for ip, attempt := range m.attempts {
		if attempt.Checking {
			continue
		}
		if !attempt.RetryAt.IsZero() && !now.Before(attempt.RetryAt) {
			delete(m.attempts, ip)
			continue
		}
		if attempt.RetryAt.IsZero() && !attempt.LastAttempt.IsZero() && !now.Before(attempt.LastAttempt.Add(loginCooldown)) {
			delete(m.attempts, ip)
		}
	}
}

func randomToken(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
