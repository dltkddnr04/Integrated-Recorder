// Package authn provides the single-administrator authentication foundation.
// It intentionally contains no HTTP routes or server path policy.
package authn

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	securityDirectory       = "security"
	adminFilename           = "admin.json"
	bootstrapTokenFilename  = "bootstrap-token"
	maxPasswordBytes        = 72 // bcrypt's input limit.
	minPasswordBytes        = 12
	maxActiveSessions       = 1024
	defaultSessionLifetime  = 12 * time.Hour
	defaultBcryptCost       = bcrypt.DefaultCost
	maxBootstrapTokenLength = 256
)

var (
	ErrBootstrapUnavailable  = errors.New("authentication bootstrap is unavailable")
	ErrInvalidBootstrapToken = errors.New("invalid bootstrap token")
	ErrInvalidPassword       = errors.New("password must be between 12 and 72 bytes")
	ErrInvalidCredentials    = errors.New("invalid credentials")
	ErrUnauthenticated       = errors.New("unauthenticated")
	ErrSessionCapacity       = errors.New("session capacity reached")
	ErrNotBootstrapped       = errors.New("administrator is not configured")
	ErrStorage               = errors.New("authentication storage failure")
	ErrCorruptStore          = errors.New("authentication store is invalid")
)

// Session is returned to the caller exactly once after login. The session
// token is omitted from JSON so callers can place it in an HttpOnly cookie;
// the CSRF token remains available for request-header validation.
type Session struct {
	Token     string    `json:"-"`
	CSRFToken string    `json:"csrf_token,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

type adminRecord struct {
	PasswordHash string `json:"password_hash"`
}

type sessionKey [sha256.Size]byte

type sessionRecord struct {
	csrfHash  [sha256.Size]byte
	expiresAt time.Time
}

// Service manages one local administrator and its process-local sessions.
// Session state is intentionally not persisted, so a process restart revokes
// every existing session.
type Service struct {
	mu sync.Mutex

	securityDir string
	adminHash   []byte
	sessions    map[sessionKey]sessionRecord

	now        func() time.Time
	bcryptCost int
}

// Open loads authentication state and prepares first-run bootstrap if no
// administrator exists. The bootstrap token is persisted in a restricted
// file and is never returned by this API.
func Open(dataRoot string) (*Service, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return nil, ErrStorage
	}
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve data root", ErrStorage)
	}
	securityDir := filepath.Join(root, securityDirectory)
	if err := os.MkdirAll(securityDir, 0700); err != nil {
		return nil, fmt.Errorf("%w: create security directory", ErrStorage)
	}
	info, err := os.Lstat(securityDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrStorage
	}
	if err := os.Chmod(securityDir, 0700); err != nil {
		return nil, fmt.Errorf("%w: secure security directory", ErrStorage)
	}

	s := &Service{
		securityDir: securityDir,
		sessions:    make(map[sessionKey]sessionRecord),
		now:         time.Now,
		bcryptCost:  defaultBcryptCost,
	}
	adminPath := filepath.Join(securityDir, adminFilename)
	hash, err := readAdminHash(adminPath)
	if err == nil {
		s.adminHash = hash
		// Once the administrator record exists, a leftover token is never
		// usable. Remove crash residue rather than allowing stale bootstrap data.
		if err := removeIfExists(filepath.Join(securityDir, bootstrapTokenFilename)); err != nil {
			return nil, fmt.Errorf("%w: remove consumed bootstrap token", ErrStorage)
		}
		if err := syncDirectory(securityDir); err != nil {
			return nil, fmt.Errorf("%w: sync security directory", ErrStorage)
		}
		return s, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := ensureBootstrapToken(securityDir); err != nil {
		return nil, err
	}
	return s, nil
}

// NeedsBootstrap reports whether the administrator has not yet been set up.
func (s *Service) NeedsBootstrap() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.adminHash) == 0
}

// BootstrapTokenRelativePath returns the stable, data-root-relative path
// where the first-run token is stored. It never returns the token itself or
// an absolute filesystem path.
func (s *Service) BootstrapTokenRelativePath() string {
	return filepath.ToSlash(filepath.Join(securityDirectory, bootstrapTokenFilename))
}

// Bootstrap installs the single administrator password if token matches the
// persisted first-run token. A successful call consumes the bootstrap token.
func (s *Service) Bootstrap(token, password string) error {
	if !validPasswordLength(password) {
		return ErrInvalidPassword
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.adminHash) != 0 {
		return ErrBootstrapUnavailable
	}

	storedToken, err := readBootstrapToken(filepath.Join(s.securityDir, bootstrapTokenFilename))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrBootstrapUnavailable
		}
		return err
	}
	providedHash := sha256.Sum256([]byte(token))
	storedHash := sha256.Sum256(storedToken)
	if subtle.ConstantTimeCompare(providedHash[:], storedHash[:]) != 1 {
		return ErrInvalidBootstrapToken
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return fmt.Errorf("%w: hash administrator password", ErrStorage)
	}
	record, err := json.Marshal(adminRecord{PasswordHash: string(hash)})
	if err != nil {
		return fmt.Errorf("%w: encode administrator record", ErrStorage)
	}
	if err := atomicWrite(filepath.Join(s.securityDir, adminFilename), append(record, '\n'), 0600); err != nil {
		return err
	}
	s.adminHash = append([]byte(nil), hash...)
	if err := removeIfExists(filepath.Join(s.securityDir, bootstrapTokenFilename)); err != nil {
		return fmt.Errorf("%w: consume bootstrap token", ErrStorage)
	}
	if err := syncDirectory(s.securityDir); err != nil {
		return fmt.Errorf("%w: sync consumed bootstrap token", ErrStorage)
	}
	return nil
}

// Login verifies the password and creates a process-local session.
func (s *Service) Login(password string) (Session, error) {
	if !validPasswordLength(password) {
		return Session{}, ErrInvalidCredentials
	}
	s.mu.Lock()
	if len(s.adminHash) == 0 {
		s.mu.Unlock()
		return Session{}, ErrNotBootstrapped
	}
	passwordHash := append([]byte(nil), s.adminHash...)
	s.mu.Unlock()
	if bcrypt.CompareHashAndPassword(passwordHash, []byte(password)) != nil {
		return Session{}, ErrInvalidCredentials
	}

	token, err := randomToken()
	if err != nil {
		return Session{}, fmt.Errorf("%w: generate session", ErrStorage)
	}
	csrf, err := randomToken()
	if err != nil {
		return Session{}, fmt.Errorf("%w: generate CSRF token", ErrStorage)
	}
	now := s.now()
	session := Session{Token: token, CSRFToken: csrf, ExpiresAt: now.Add(defaultSessionLifetime)}
	tokenKey := hashSessionToken(token)
	csrfHash := sha256.Sum256([]byte(csrf))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	if len(s.sessions) >= maxActiveSessions {
		return Session{}, ErrSessionCapacity
	}
	s.sessions[tokenKey] = sessionRecord{csrfHash: csrfHash, expiresAt: session.ExpiresAt}
	return session, nil
}

// Authenticate validates a session token. Returned Session intentionally
// omits the token and CSRF secret; those are only available from Login.
func (s *Service) Authenticate(token string) (Session, error) {
	if token == "" {
		return Session{}, ErrUnauthenticated
	}
	key := hashSessionToken(token)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	record, ok := s.sessions[key]
	if !ok {
		return Session{}, ErrUnauthenticated
	}
	return Session{ExpiresAt: record.expiresAt}, nil
}

// Logout revokes a session. Unknown and empty tokens are harmless.
func (s *Service) Logout(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	delete(s.sessions, hashSessionToken(token))
	s.mu.Unlock()
}

// ValidCSRF compares the supplied value with the CSRF token bound to a live
// session token. Both values are hashed before constant-time comparison.
func (s *Service) ValidCSRF(sessionToken, supplied string) bool {
	if sessionToken == "" {
		return false
	}
	key := hashSessionToken(sessionToken)
	suppliedHash := sha256.Sum256([]byte(supplied))
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, ok := s.sessions[key]; ok && !now.Before(record.expiresAt) {
		delete(s.sessions, key)
		return false
	} else if ok {
		return subtle.ConstantTimeCompare(record.csrfHash[:], suppliedHash[:]) == 1
	}
	return false
}

func (s *Service) cleanupExpiredLocked(now time.Time) {
	for key, record := range s.sessions {
		if !now.Before(record.expiresAt) {
			delete(s.sessions, key)
		}
	}
}

func validPasswordLength(password string) bool {
	return len(password) >= minPasswordBytes && len(password) <= maxPasswordBytes
}

func randomToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func hashSessionToken(token string) sessionKey {
	return sha256.Sum256([]byte(token))
}

func readAdminHash(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("%w: inspect administrator record", ErrStorage)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrCorruptStore
	}
	if info.Size() <= 0 || info.Size() > 4096 {
		return nil, ErrCorruptStore
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, fmt.Errorf("%w: secure administrator record", ErrStorage)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read administrator record", ErrStorage)
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 4096))
	decoder.DisallowUnknownFields()
	var record adminRecord
	if err := decoder.Decode(&record); err != nil || record.PasswordHash == "" {
		return nil, ErrCorruptStore
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrCorruptStore
	}
	hash := []byte(record.PasswordHash)
	if _, err := bcrypt.Cost(hash); err != nil {
		return nil, ErrCorruptStore
	}
	return hash, nil
}

func ensureBootstrapToken(securityDir string) error {
	path := filepath.Join(securityDir, bootstrapTokenFilename)
	token, err := randomToken()
	if err != nil {
		return fmt.Errorf("%w: generate bootstrap token", ErrStorage)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		if _, readErr := readBootstrapToken(path); readErr != nil {
			return readErr
		}
		if chmodErr := os.Chmod(path, 0600); chmodErr != nil {
			return fmt.Errorf("%w: secure bootstrap token", ErrStorage)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: create bootstrap token", ErrStorage)
	}
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = os.Remove(path)
		}
	}()
	if _, err := io.WriteString(f, token); err != nil {
		return fmt.Errorf("%w: write bootstrap token", ErrStorage)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("%w: sync bootstrap token", ErrStorage)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("%w: close bootstrap token", ErrStorage)
	}
	if err := syncDirectory(securityDir); err != nil {
		return fmt.Errorf("%w: sync security directory", ErrStorage)
	}
	cleanup = false
	return nil
}

func readBootstrapToken(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("%w: inspect bootstrap token", ErrStorage)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxBootstrapTokenLength {
		return nil, ErrCorruptStore
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, fmt.Errorf("%w: secure bootstrap token", ErrStorage)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read bootstrap token", ErrStorage)
	}
	if len(data) == 0 || len(data) > maxBootstrapTokenLength {
		return nil, ErrCorruptStore
	}
	return data, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) (retErr error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".authn-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: create temporary record", ErrStorage)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("%w: set record permissions", ErrStorage)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("%w: write record", ErrStorage)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("%w: sync record", ErrStorage)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: close record", ErrStorage)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("%w: publish record", ErrStorage)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("%w: sync record directory", ErrStorage)
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
