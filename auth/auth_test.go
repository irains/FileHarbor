package auth

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const testPassword = "a durable password"

func testConfig(t *testing.T) Config {
	t.Helper()
	hash, err := GeneratePasswordHash([]byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Username:      "admin",
		PasswordHash:  hash,
		SessionSecret: "0123456789abcdef0123456789abcdef",
		APIToken:      "abcdef0123456789abcdef0123456789",
	}
}

func TestConfigFromLookupUsesOneNamespace(t *testing.T) {
	primary := map[string]string{
		"FILEHARBOR_ADMIN_USERNAME":      "admin",
		"FILEHARBOR_ADMIN_PASSWORD_HASH": testConfig(t).PasswordHash,
		"FILEHARBOR_SESSION_SECRET":      "0123456789abcdef0123456789abcdef",
		"FILEHARBOR_API_TOKEN":           "abcdef0123456789abcdef0123456789",
	}
	lookup := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	config, err := ConfigFromLookup(lookup(primary), true)
	if err != nil || config.Username != "admin" || !config.CookieSecure {
		t.Fatalf("primary configuration = %#v, %v", config, err)
	}

	legacy := make(map[string]string, len(primary))
	for key, value := range primary {
		legacy["GOFILE_"+strings.TrimPrefix(key, "FILEHARBOR_")] = value
	}
	config, err = ConfigFromLookup(lookup(legacy), false)
	if err != nil || config.Username != "admin" || config.CookieSecure {
		t.Fatalf("legacy configuration = %#v, %v", config, err)
	}
	for key, value := range primary {
		legacy[key] = value
	}
	if _, err := ConfigFromLookup(lookup(legacy), false); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("mixed namespaces error = %v", err)
	}
}

func TestGeneratePasswordHash(t *testing.T) {
	first, err := GeneratePasswordHash([]byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	second, err := GeneratePasswordHash([]byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("bcrypt hashes must use distinct salts")
	}
	cost, err := bcrypt.Cost([]byte(first))
	if err != nil || cost != PasswordHashCost {
		t.Fatalf("bcrypt cost = %d, %v", cost, err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(first), []byte(testPassword)); err != nil {
		t.Fatal(err)
	}
	for _, password := range [][]byte{nil, []byte(strings.Repeat("x", maxPasswordBytes+1))} {
		if _, err := GeneratePasswordHash(password); !errors.Is(err, ErrInvalidPassword) && !errors.Is(err, ErrPasswordTooLong) {
			t.Fatalf("invalid password error = %v", err)
		}
	}
}

func TestConfigRejectsInvalidPasswordHashes(t *testing.T) {
	config := testConfig(t)
	for _, mutate := range []func(*Config){
		func(c *Config) { c.PasswordHash = "" },
		func(c *Config) { c.PasswordHash = "not-a-bcrypt-hash" },
		func(c *Config) { c.Username = " " },
		func(c *Config) { c.SessionSecret = "short" },
		func(c *Config) { c.APIToken = "short" },
	} {
		candidate := config
		mutate(&candidate)
		if !errors.Is(candidate.Validate(), ErrInvalidConfig) {
			t.Fatalf("invalid configuration was accepted")
		}
	}

	weak, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	config.PasswordHash = string(weak)
	if !errors.Is(config.Validate(), ErrInvalidConfig) {
		t.Fatal("weak bcrypt hash was accepted")
	}

	tooStrong, err := bcrypt.GenerateFromPassword([]byte(testPassword), maxPasswordHashCost+1)
	if err != nil {
		t.Fatal(err)
	}
	config.PasswordHash = string(tooStrong)
	if !errors.Is(config.Validate(), ErrInvalidConfig) {
		t.Fatal("impractically expensive bcrypt hash was accepted")
	}
}

func TestNewManagerRejectsInvalidCookiePath(t *testing.T) {
	for _, path := range []string{"relative", "/gofile/", "/gofile//files", "/gofile/../admin", "/gofile;admin", "/gofile?x=1", "/gofile/管理", "/gofile\x01admin"} {
		config := testConfig(t)
		config.CookiePath = path
		if _, err := NewManager(config); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("CookiePath %q error = %v, want invalid configuration", path, err)
		}
	}
}

func TestSessionCookiesUseConfiguredPath(t *testing.T) {
	config := testConfig(t)
	config.CookiePath = "/gofile"
	config.CookieSecure = true
	manager, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	cookie := manager.Cookie("signed", time.Now().Add(time.Hour))
	if cookie.Name != CookieName || cookie.Path != "/gofile" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie = %#v", cookie)
	}
	expired := manager.ExpiredCookie()
	if expired.Name != CookieName || expired.Path != "/gofile" || expired.MaxAge != -1 || !expired.Secure || !expired.HttpOnly || expired.SameSite != http.SameSiteStrictMode {
		t.Fatalf("expired cookie = %#v", expired)
	}
	legacy := manager.ExpiredLegacyCookie()
	if legacy.Name != LegacyCookieName || legacy.Path != "/gofile" || legacy.MaxAge != -1 || !legacy.Secure || !legacy.HttpOnly || legacy.SameSite != http.SameSiteStrictMode {
		t.Fatalf("expired legacy cookie = %#v", legacy)
	}

	rootManager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if rootManager.Cookie("signed", time.Now()).Path != "/" || rootManager.ExpiredCookie().Path != "/" || rootManager.ExpiredLegacyCookie().Path != "/" {
		t.Fatal("empty cookie path should default to root")
	}
}

func TestLoginSessionAndLogout(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	info, signed, expiry, err := manager.Login("127.0.0.1", "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if info.CSRF == "" || expiry.Sub(time.Now()) < 11*time.Hour {
		t.Fatal("expected a durable session and csrf token")
	}
	request, _ := http.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(manager.Cookie(signed, expiry))
	fromRequest, ok := manager.SessionFromRequest(request)
	if !ok || fromRequest.Username != "admin" || !manager.CheckCSRF(fromRequest, info.CSRF) {
		t.Fatal("expected signed session cookie to authenticate")
	}
	manager.Logout(fromRequest)
	if _, ok := manager.SessionFromRequest(request); ok {
		t.Fatal("logout should invalidate session")
	}
}

func TestLoginRejectsWrongUsernameAndPassword(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, credentials := range [][2]string{{"wrong", testPassword}, {"admin", "wrong"}} {
		if _, _, _, err := manager.Login("127.0.0.1", credentials[0], credentials[1]); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("login error = %v", err)
		}
	}
}

func TestLoginChecksPasswordOutsideManagerLock(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	manager.comparePassword = func([]byte, []byte) error {
		close(started)
		<-release
		return errors.New("wrong password")
	}
	go func() {
		_, _, _, _ = manager.Login("127.0.0.1", "wrong", "wrong")
		close(finished)
	}()
	<-started

	acquired := make(chan struct{})
	go func() {
		manager.mu.Lock()
		close(acquired)
		manager.mu.Unlock()
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		close(release)
		<-finished
		t.Fatal("bcrypt comparison held the manager lock")
	}
	close(release)
	<-finished
}

func TestLoginChecksPasswordForWrongUsername(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	manager.comparePassword = func([]byte, []byte) error {
		called = true
		return errors.New("wrong password")
	}
	if _, _, _, err := manager.Login("127.0.0.1", "wrong", testPassword); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login error = %v", err)
	}
	if !called {
		t.Fatal("wrong username skipped password verification")
	}
}

func TestLoginClearsCheckingAfterPasswordComparison(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	manager.comparePassword = func([]byte, []byte) error { return errors.New("wrong password") }
	if _, _, _, err := manager.Login("127.0.0.1", "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("first login error = %v", err)
	}
	if _, _, _, err := manager.Login("127.0.0.1", "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("follow-up login error = %v", err)
	}
}

func TestLoginRejectsInvalidPasswordInput(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"", strings.Repeat("x", maxPasswordBytes+1)} {
		if _, _, _, err := manager.Login("127.0.0.1", "admin", password); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("password length %d error = %v", len(password), err)
		}
	}
}

func TestLoginAllowsOnlyOneCheckPerIP(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	manager.comparePassword = func([]byte, []byte) error {
		close(started)
		<-release
		return errors.New("wrong password")
	}
	go func() {
		_, _, _, err := manager.Login("127.0.0.1", "admin", "wrong")
		finished <- err
	}()
	<-started

	if _, _, _, err := manager.Login("127.0.0.1", "admin", "wrong"); !errors.Is(err, ErrRateLimited) {
		close(release)
		<-finished
		t.Fatalf("concurrent login error = %v", err)
	}
	close(release)
	if err := <-finished; !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("first login error = %v", err)
	}
}

func TestLoginLimitsConcurrentPasswordChecks(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, maxConcurrentPasswordChecks)
	release := make(chan struct{})
	finished := make(chan error, maxConcurrentPasswordChecks)
	var mu sync.Mutex
	checks := 0
	manager.comparePassword = func([]byte, []byte) error {
		mu.Lock()
		checks++
		mu.Unlock()
		started <- struct{}{}
		<-release
		return errors.New("wrong password")
	}
	for i := 0; i < maxConcurrentPasswordChecks; i++ {
		go func(i int) {
			_, _, _, err := manager.Login("ip-"+strconv.Itoa(i), "admin", "wrong")
			finished <- err
		}(i)
	}
	for i := 0; i < maxConcurrentPasswordChecks; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("password checks did not reach the configured limit")
		}
	}
	if _, _, _, err := manager.Login("overflow", "admin", "wrong"); !errors.Is(err, ErrRateLimited) {
		close(release)
		for i := 0; i < maxConcurrentPasswordChecks; i++ {
			<-finished
		}
		t.Fatalf("over-limit login error = %v", err)
	}
	mu.Lock()
	checkCount := checks
	mu.Unlock()
	if checkCount != maxConcurrentPasswordChecks {
		close(release)
		for i := 0; i < maxConcurrentPasswordChecks; i++ {
			<-finished
		}
		t.Fatalf("password comparisons = %d, want %d", checkCount, maxConcurrentPasswordChecks)
	}
	close(release)
	for i := 0; i < maxConcurrentPasswordChecks; i++ {
		if err := <-finished; !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("completed login error = %v", err)
		}
	}
}

func TestLoginClearsCheckingAfterPasswordCheckCapacityIsFull(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(manager.passwordChecks); i++ {
		manager.passwordChecks <- struct{}{}
	}
	if _, _, _, err := manager.Login("127.0.0.1", "admin", "wrong"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("saturated login error = %v", err)
	}
	manager.mu.Lock()
	attempt, tracked := manager.attempts["127.0.0.1"]
	manager.mu.Unlock()
	if !tracked || attempt.Checking {
		t.Fatalf("checking state after saturation = %#v, tracked = %t", attempt, tracked)
	}
	for i := 0; i < cap(manager.passwordChecks); i++ {
		<-manager.passwordChecks
	}
	manager.comparePassword = func([]byte, []byte) error { return errors.New("wrong password") }
	if _, _, _, err := manager.Login("127.0.0.1", "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("login after released capacity error = %v", err)
	}
}

func TestBearerToken(t *testing.T) {
	config := testConfig(t)
	manager, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	if !manager.IsBearerToken("Bearer " + config.APIToken) {
		t.Fatal("expected valid token")
	}
	if manager.IsBearerToken("Bearer wrong") {
		t.Fatal("wrong token must fail")
	}
}

func TestLoginAttemptTrackingIsBounded(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	manager.comparePassword = func([]byte, []byte) error { return errors.New("wrong password") }
	for i := 0; i < maxTrackedLoginIPs; i++ {
		if _, _, _, err := manager.Login("ip-"+strconv.Itoa(i), "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d = %v", i, err)
		}
	}
	if _, _, _, err := manager.Login("overflow", "admin", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected oldest-attempt eviction, got %v", err)
	}
	if len(manager.attempts) != maxTrackedLoginIPs {
		t.Fatalf("attempt tracking grew to %d entries", len(manager.attempts))
	}
}

func TestLoginDoesNotEvictInFlightAttempt(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	manager.comparePassword = func([]byte, []byte) error { return errors.New("wrong password") }
	for i := 0; i < maxTrackedLoginIPs; i++ {
		manager.mu.Lock()
		manager.attempts["ip-"+strconv.Itoa(i)] = loginAttempt{
			LastAttempt: time.Unix(int64(i+1), 0),
			Checking:    true,
		}
		manager.mu.Unlock()
	}

	if _, _, _, err := manager.Login("overflow", "admin", "wrong"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("full in-flight tracking error = %v", err)
	}
}

func TestArchiveTicketIsSessionBoundAndOneUse(t *testing.T) {
	manager, err := NewManager(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := manager.IssueArchiveTicket("session-a", []ArchiveItem{{Path: "a.txt", Version: "v1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.ConsumeArchiveTicket("session-b", ticket); ok {
		t.Fatal("ticket must be bound to session")
	}
	items, ok := manager.ConsumeArchiveTicket("session-a", ticket)
	if !ok || len(items) != 1 {
		t.Fatal("ticket should work once for issuing session")
	}
	if _, ok := manager.ConsumeArchiveTicket("session-a", ticket); ok {
		t.Fatal("ticket must be one-use")
	}
}

func TestRememberedSessionLifetimeAndRevocation(t *testing.T) {
	for _, remember := range []bool{false, true} {
		manager, err := NewManager(testConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().Truncate(time.Second)
		manager.now = func() time.Time { return now }
		info, signed, expiry, err := manager.LoginWithRemember("127.0.0.1", "admin", testPassword, remember)
		if err != nil {
			t.Fatal(err)
		}
		duration := SessionDuration
		if remember {
			duration = RememberedSessionDuration
		}
		if !expiry.Equal(now.Add(duration)) {
			t.Fatal("unexpected absolute expiry")
		}
		cookie := manager.LoginCookie(signed, expiry, remember)
		if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
			t.Fatal("cookie protections missing")
		}
		if remember {
			if cookie.MaxAge != int(duration.Seconds()) || !cookie.Expires.Equal(expiry) {
				t.Fatal("persistent cookie expiry mismatch")
			}
		} else if cookie.MaxAge != 0 || !cookie.Expires.IsZero() {
			t.Fatal("ordinary cookie persisted")
		}
		request, _ := http.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(cookie)
		now = expiry.Add(-time.Second)
		if _, ok := manager.SessionFromRequest(request); !ok {
			t.Fatal("session expired early")
		}
		now = expiry
		if _, ok := manager.SessionFromRequest(request); ok {
			t.Fatal("expired session accepted")
		}
		now = expiry.Add(time.Second)
		info, signed, expiry, err = manager.LoginWithRemember("127.0.0.1", "admin", testPassword, remember)
		if err != nil {
			t.Fatal(err)
		}
		request, _ = http.NewRequest(http.MethodGet, "/", nil)
		request.AddCookie(manager.LoginCookie(signed, expiry, remember))
		manager.Logout(info)
		if _, ok := manager.SessionFromRequest(request); ok {
			t.Fatal("logout did not revoke session")
		}
	}
}
