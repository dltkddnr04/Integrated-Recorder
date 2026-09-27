package authn

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const testPassword = "correct-horse-battery"

func openForTest(t *testing.T, root string) (*Service, string) {
	t.Helper()
	service, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	service.bcryptCost = bcrypt.MinCost
	bootstrapPath := filepath.Join(root, filepath.FromSlash(service.BootstrapTokenRelativePath()))
	token, err := os.ReadFile(bootstrapPath)
	if err != nil {
		t.Fatalf("read test bootstrap token: %v", err)
	}
	return service, string(token)
}

func bootstrapForTest(t *testing.T, service *Service, token string) {
	t.Helper()
	if err := service.Bootstrap(token, testPassword); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
}

func TestFirstRunTokenPermissionsPersistenceAndNoDisclosure(t *testing.T) {
	root := t.TempDir()
	service, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if !service.NeedsBootstrap() {
		t.Fatal("new installation does not need bootstrap")
	}
	if got := service.BootstrapTokenRelativePath(); got != "security/bootstrap-token" {
		t.Fatalf("bootstrap token path = %q", got)
	}
	tokenPath := filepath.Join(root, filepath.FromSlash(service.BootstrapTokenRelativePath()))
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) == 0 || strings.Contains(service.BootstrapTokenRelativePath(), string(token)) {
		t.Fatal("token missing or disclosed by path API")
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(root, "security"): 0700,
		tokenPath:                       0600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %#o, want %#o", filepath.Base(path), got, want)
		}
	}

	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	reopened.bcryptCost = bcrypt.MinCost
	tokenAgain, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(tokenAgain) != string(token) {
		t.Fatal("reopen rotated the persisted bootstrap token")
	}
}

func TestBootstrapRejectsWrongTokenConsumesTokenAndRejectsReplay(t *testing.T) {
	root := t.TempDir()
	service, token := openForTest(t, root)
	tokenPath := filepath.Join(root, filepath.FromSlash(service.BootstrapTokenRelativePath()))
	wrong := "wrong bootstrap token"
	if err := service.Bootstrap(wrong, testPassword); !errors.Is(err, ErrInvalidBootstrapToken) {
		t.Fatalf("wrong token error = %v", err)
	}
	if err := service.Bootstrap(token, testPassword); err != nil {
		t.Fatalf("valid bootstrap: %v", err)
	}
	if service.NeedsBootstrap() {
		t.Fatal("bootstrap did not install administrator")
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap token still exists: %v", err)
	}
	if err := service.Bootstrap(token, testPassword); !errors.Is(err, ErrBootstrapUnavailable) {
		t.Fatalf("bootstrap replay error = %v", err)
	}
	if strings.Contains(ErrInvalidBootstrapToken.Error(), token) || strings.Contains(ErrInvalidBootstrapToken.Error(), testPassword) {
		t.Fatal("auth error disclosed sensitive input")
	}
}

func TestPasswordPolicyAndBcryptPersistence(t *testing.T) {
	root := t.TempDir()
	service, token := openForTest(t, root)
	for _, password := range []string{"short-pass", strings.Repeat("x", 73)} {
		if err := service.Bootstrap(token, password); !errors.Is(err, ErrInvalidPassword) {
			t.Errorf("Bootstrap(%d bytes) error = %v", len(password), err)
		}
	}
	bootstrapForTest(t, service, token)
	data, err := os.ReadFile(filepath.Join(root, "security", adminFilename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), testPassword) {
		t.Fatal("plaintext password persisted")
	}
	var record adminRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if cost, err := bcrypt.Cost([]byte(record.PasswordHash)); err != nil || cost != bcrypt.MinCost {
		t.Fatalf("persisted password hash invalid: cost=%d err=%v", cost, err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte(testPassword)); err != nil {
		t.Fatalf("persisted bcrypt hash does not verify: %v", err)
	}
	if _, err := service.Login("wrong-password-123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v", err)
	}
	if _, err := service.Login(strings.Repeat("x", 73)); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("overlong login error = %v", err)
	}
}

func TestLoginAuthenticateLogoutAndCSRF(t *testing.T) {
	root := t.TempDir()
	service, token := openForTest(t, root)
	bootstrapForTest(t, service, token)
	session, err := service.Login(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Token) < 40 || len(session.CSRFToken) < 40 || !session.ExpiresAt.After(time.Now()) {
		t.Fatalf("invalid session response: %#v", session)
	}
	if _, err := service.Authenticate(session.Token); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !service.ValidCSRF(session.Token, session.CSRFToken) {
		t.Fatal("valid CSRF token rejected")
	}
	if service.ValidCSRF(session.Token, session.CSRFToken+"x") || service.ValidCSRF(session.Token, "") {
		t.Fatal("invalid CSRF token accepted")
	}
	service.Logout(session.Token)
	if _, err := service.Authenticate(session.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Authenticate after logout error = %v", err)
	}
	if service.ValidCSRF(session.Token, session.CSRFToken) {
		t.Fatal("CSRF validation succeeded after logout")
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), session.Token) || !strings.Contains(string(encoded), session.CSRFToken) {
		t.Fatal("session JSON must hide the cookie token and expose the CSRF token")
	}
}

func TestSessionExpiryAndRestartInvalidation(t *testing.T) {
	root := t.TempDir()
	service, token := openForTest(t, root)
	bootstrapForTest(t, service, token)
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	session, err := service.Login(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(defaultSessionLifetime + time.Second)
	if _, err := service.Authenticate(session.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired session error = %v", err)
	}
	if got := len(service.sessions); got != 0 {
		t.Fatalf("expired session was not cleaned up: %d entries", got)
	}

	service.now = time.Now
	restartSession, err := service.Login(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.NeedsBootstrap() {
		t.Fatal("restart lost configured administrator")
	}
	if _, err := reopened.Authenticate(restartSession.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("restart retained session: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "security", bootstrapTokenFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed token exists after restart: %v", err)
	}
}

func TestConcurrentSessionCapacityAndExpiredCleanup(t *testing.T) {
	root := t.TempDir()
	service, token := openForTest(t, root)
	bootstrapForTest(t, service, token)
	now := time.Now()
	service.now = func() time.Time { return now }
	service.mu.Lock()
	for i := 0; i < maxActiveSessions-1; i++ {
		var tokenHash, csrfHash [sha256.Size]byte
		tokenHash[0], tokenHash[1] = byte(i>>8), byte(i)
		csrfHash[0] = byte(i + 1)
		service.sessions[tokenHash] = sessionRecord{csrfHash: csrfHash, expiresAt: now.Add(time.Hour)}
	}
	service.mu.Unlock()

	const callers = 16
	var wg sync.WaitGroup
	wg.Add(callers)
	var successes, capacityErrors int
	var resultMu sync.Mutex
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			_, err := service.Login(testPassword)
			resultMu.Lock()
			defer resultMu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrSessionCapacity):
				capacityErrors++
			default:
				t.Errorf("Login: %v", err)
			}
		}()
	}
	wg.Wait()
	if successes != 1 || capacityErrors != callers-1 || len(service.sessions) != maxActiveSessions {
		t.Fatalf("success=%d capacity=%d sessions=%d", successes, capacityErrors, len(service.sessions))
	}

	service.mu.Lock()
	for key, record := range service.sessions {
		record.expiresAt = now.Add(-time.Second)
		service.sessions[key] = record
	}
	service.mu.Unlock()
	if _, err := service.Login(testPassword); err != nil {
		t.Fatalf("login after expired-session cleanup: %v", err)
	}
	if len(service.sessions) != 1 {
		t.Fatalf("lazy cleanup left %d sessions", len(service.sessions))
	}
}

func TestSessionCookieFlagsAndExtraction(t *testing.T) {
	session := Session{Token: "session-secret", ExpiresAt: time.Now().Add(time.Hour)}
	for _, tc := range []struct {
		name       string
		tls        bool
		force      bool
		wantSecure bool
	}{
		{name: "plain"},
		{name: "TLS", tls: true, wantSecure: true},
		{name: "forced", force: true, wantSecure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
			if tc.tls {
				request.TLS = &tls.ConnectionState{}
			}
			recorder := httptest.NewRecorder()
			SetSessionCookie(recorder, request, session, tc.force)
			response := recorder.Result()
			cookie, err := responseCookie(response, SessionCookieName)
			if err != nil {
				t.Fatal(err)
			}
			if cookie.Value != session.Token || cookie.Path != "/" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Secure != tc.wantSecure || cookie.MaxAge <= 0 {
				t.Fatalf("cookie attributes: %#v", cookie)
			}
			if got := SessionToken(requestWithCookie(request, cookie)); got != session.Token {
				t.Fatalf("SessionToken = %q", got)
			}
			clear := httptest.NewRecorder()
			ClearSessionCookie(clear, request, tc.force)
			cleared, err := responseCookie(clear.Result(), SessionCookieName)
			if err != nil {
				t.Fatal(err)
			}
			if cleared.MaxAge != -1 || !cleared.HttpOnly || cleared.Secure != tc.wantSecure || cleared.SameSite != http.SameSiteStrictMode {
				t.Fatalf("clear cookie attributes: %#v", cleared)
			}
		})
	}
}

func requestWithCookie(request *http.Request, cookie *http.Cookie) *http.Request {
	clone := request.Clone(request.Context())
	clone.AddCookie(cookie)
	return clone
}

func responseCookie(response *http.Response, name string) (*http.Cookie, error) {
	for _, cookie := range response.Cookies() {
		if cookie.Name == name {
			return cookie, nil
		}
	}
	return nil, http.ErrNoCookie
}
