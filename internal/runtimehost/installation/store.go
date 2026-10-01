// Package installation stores Runtime Host-owned first-run lifecycle state.
// This state is intentionally separate from application generations and the
// canonical recording archive.
package installation

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	SchemaVersion = 1
	Filename      = "installation.json"
	maxRecordSize = 4096
)

type State string

const (
	StateUninitialized    State = "uninitialized"
	StateSetupInProgress  State = "setup_in_progress"
	StateReady            State = "ready"
	StateRecoveryRequired State = "recovery_required"
)

var (
	ErrInvalidTransition = errors.New("installation state transition is invalid")
	ErrRecoveryRequired  = errors.New("installation recovery is required")
	ErrAlreadyReady      = errors.New("installation is already ready")
	ErrInvalidState      = errors.New("installation state is invalid")
	installationIDRE     = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

// Record is the complete persisted installation state. It contains no
// credentials, process identities, filesystem locators, or product settings.
type Record struct {
	SchemaVersion  int       `json:"schema_version"`
	InstallationID string    `json:"installation_id"`
	State          State     `json:"state"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ReadyAt        time.Time `json:"ready_at,omitempty"`
}

// Snapshot includes a constrained diagnostic code for the public setup
// projection. DiagnosticCode is never an error string.
type Snapshot struct {
	Record
	DiagnosticCode string `json:"diagnostic_code,omitempty"`
}

type AdminStatus uint8

const (
	AdminUnknown AdminStatus = iota
	AdminMissing
	AdminConfigured
)

type Store struct {
	mu         sync.Mutex
	path       string
	record     Record
	diagnostic string
	corrupt    bool
	write      func(string, Record) error
}

// Reconcile creates or validates the Runtime Host installation record. The
// caller must inspect administrator state without side effects first and pass
// its bounded result here. An absent state plus an existing administrator is
// the legacy-install migration; an existing uninitialized state plus an
// administrator means bootstrap completed and the wizard must resume.
func Reconcile(dataRoot string, admin AdminStatus, authDisabled bool) (*Store, error) {
	if strings.TrimSpace(dataRoot) == "" || !filepath.IsAbs(dataRoot) || filepath.Clean(dataRoot) != dataRoot {
		return nil, errors.New("installation data root is invalid")
	}
	runtimeDir := filepath.Join(dataRoot, "runtime")
	if err := ensurePrivateDir(runtimeDir); err != nil {
		return nil, errors.New("installation runtime directory is unavailable")
	}
	s := &Store{path: filepath.Join(runtimeDir, Filename), write: writeRecord}
	record, err := readRecord(s.path)
	if errors.Is(err, os.ErrNotExist) {
		now := time.Now().UTC()
		state := StateUninitialized
		if authDisabled || admin == AdminConfigured {
			// With no prior Host state, an existing admin is a legacy installation.
			// AUTH_DISABLED is an explicit loopback-only operator mode.
			state = StateReady
		}
		if admin == AdminUnknown && !authDisabled {
			state = StateRecoveryRequired
			s.diagnostic = "administrator_state_unavailable"
		}
		if state == StateReady {
			readyAt := now
			id, idErr := newInstallationID()
			if idErr != nil {
				return nil, errors.New("installation identity could not be created")
			}
			record = Record{SchemaVersion: SchemaVersion, InstallationID: id, State: state, CreatedAt: now, UpdatedAt: now, ReadyAt: readyAt}
		} else {
			id, idErr := newInstallationID()
			if idErr != nil {
				return nil, errors.New("installation identity could not be created")
			}
			record = Record{SchemaVersion: SchemaVersion, InstallationID: id, State: state, CreatedAt: now, UpdatedAt: now}
		}
		if err := s.write(s.path, record); err != nil {
			return nil, errors.New("installation state could not be initialized")
		}
		s.record = record
		return s, nil
	}
	if err != nil {
		// Corrupt, linked, unknown-version, and non-regular state is fail-closed.
		// Keep the original bytes untouched and expose only a safe code.
		s.corrupt = true
		s.diagnostic = "installation_state_corrupt"
		s.record = recoveryRecord()
		return s, nil
	}
	s.record = record
	if record.State == StateRecoveryRequired {
		s.diagnostic = "installation_recovery_required"
		return s, nil
	}
	if !authDisabled {
		switch record.State {
		case StateReady:
			if admin != AdminConfigured {
				if err := s.setRecoveryLocked("administrator_state_missing"); err != nil {
					// The in-memory view remains recovery-required even if its durable
					// marker cannot be published; it must never start automation.
					s.record.State = StateRecoveryRequired
					s.diagnostic = "administrator_state_missing"
				}
			}
		case StateUninitialized:
			switch admin {
			case AdminConfigured:
				if _, err := s.transitionLocked(StateSetupInProgress); err != nil {
					return nil, errors.New("installation setup state could not be reconciled")
				}
			case AdminUnknown:
				s.setRecoveryBestEffort("administrator_state_unavailable")
			}
		case StateSetupInProgress:
			if admin != AdminConfigured {
				s.setRecoveryBestEffort("administrator_state_missing")
			}
		}
	}
	if authDisabled && record.State != StateReady {
		if _, err := s.transitionLocked(StateReady); err != nil {
			return nil, errors.New("installation could not enter ready state")
		}
	}
	return s, nil
}

// ReadOnly returns a bounded state projection without creating directories or
// repairing state. Missing/corrupt state is represented as recovery-required.
func ReadOnly(dataRoot string) Snapshot {
	path := filepath.Join(dataRoot, "runtime", Filename)
	record, err := readRecord(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Snapshot{Record: Record{SchemaVersion: SchemaVersion, State: StateUninitialized}}
		}
		return Snapshot{Record: recoveryRecord(), DiagnosticCode: "installation_state_corrupt"}
	}
	return Snapshot{Record: record, DiagnosticCode: diagnosticFor(record.State)}
}

func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	if s.corrupt {
		return Snapshot{Record: s.record, DiagnosticCode: "installation_state_corrupt"}
	}
	return Snapshot{Record: s.record, DiagnosticCode: s.diagnostic}
}

func (s *Store) Ready() bool {
	return s.Snapshot().State == StateReady
}

// Begin changes uninitialized -> setup_in_progress after administrator
// bootstrap. Repeated begin calls during setup are idempotent.
func (s *Store) Begin(adminConfigured bool) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	if s.corrupt || s.record.State == StateRecoveryRequired {
		return s.snapshotLocked(), ErrRecoveryRequired
	}
	if s.record.State == StateSetupInProgress {
		return s.snapshotLocked(), nil
	}
	if s.record.State != StateUninitialized || !adminConfigured {
		return s.snapshotLocked(), ErrInvalidTransition
	}
	if _, err := s.transitionLocked(StateSetupInProgress); err != nil {
		return s.snapshotLocked(), err
	}
	return s.snapshotLocked(), nil
}

// Complete atomically commits setup_in_progress -> ready. Calling it again
// after a successful commit is idempotent so Host can reconcile a lost
// post-commit Control notification.
func (s *Store) Complete() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshLocked()
	if s.corrupt || s.record.State == StateRecoveryRequired {
		return s.snapshotLocked(), ErrRecoveryRequired
	}
	if s.record.State == StateReady {
		return s.snapshotLocked(), nil
	}
	if s.record.State != StateSetupInProgress {
		return s.snapshotLocked(), ErrInvalidTransition
	}
	if _, err := s.transitionLocked(StateReady); err != nil {
		return s.snapshotLocked(), err
	}
	return s.snapshotLocked(), nil
}

// MarkRecovery persists a safe recovery state where the existing canonical
// record was readable. A corrupt record is never overwritten.
func (s *Store) MarkRecovery(code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.corrupt {
		return ErrRecoveryRequired
	}
	if !validDiagnostic(code) {
		code = "installation_recovery_required"
	}
	if s.record.State != StateRecoveryRequired {
		if _, err := s.transitionLocked(StateRecoveryRequired); err != nil {
			return err
		}
	}
	s.diagnostic = code
	return nil
}

func (s *Store) setRecoveryBestEffort(code string) {
	if err := s.setRecoveryLocked(code); err != nil {
		s.record.State = StateRecoveryRequired
		s.diagnostic = code
	}
}

func (s *Store) setRecoveryLocked(code string) error {
	if !validDiagnostic(code) {
		code = "installation_recovery_required"
	}
	if s.record.State != StateRecoveryRequired {
		if _, err := s.transitionLocked(StateRecoveryRequired); err != nil {
			return err
		}
	}
	s.diagnostic = code
	return nil
}

func (s *Store) transitionLocked(next State) (Record, error) {
	if s.corrupt || !allowedTransition(s.record.State, next) {
		return s.record, ErrInvalidTransition
	}
	updated := s.record
	updated.State = next
	updated.UpdatedAt = time.Now().UTC()
	if next == StateReady {
		updated.ReadyAt = updated.UpdatedAt
	} else {
		updated.ReadyAt = time.Time{}
	}
	if err := s.write(s.path, updated); err != nil {
		return s.record, errors.New("installation state could not be saved")
	}
	s.record = updated
	s.diagnostic = diagnosticFor(next)
	return updated, nil
}

func (s *Store) snapshotLocked() Snapshot {
	return Snapshot{Record: s.record, DiagnosticCode: s.diagnostic}
}

// refreshLocked revalidates the durable record before the Host makes a public
// readiness decision or state transition. The file lives in a private runtime
// directory, but fail-closed behavior also covers runtime corruption or
// accidental removal after startup.
func (s *Store) refreshLocked() {
	if s.corrupt {
		return
	}
	record, err := readRecord(s.path)
	if err != nil || record.InstallationID != s.record.InstallationID {
		s.corrupt = true
		s.record = recoveryRecord()
		s.diagnostic = "installation_state_corrupt"
		return
	}
	previousDiagnostic := s.diagnostic
	s.record = record
	s.diagnostic = diagnosticFor(record.State)
	if record.State == StateRecoveryRequired && previousDiagnostic != "" {
		s.diagnostic = previousDiagnostic
	}
}

func allowedTransition(from, to State) bool {
	switch from {
	case StateUninitialized:
		return to == StateSetupInProgress || to == StateReady || to == StateRecoveryRequired
	case StateSetupInProgress:
		return to == StateReady || to == StateRecoveryRequired
	case StateReady:
		return to == StateRecoveryRequired
	default:
		return false
	}
}

func diagnosticFor(state State) string {
	if state == StateRecoveryRequired {
		return "installation_recovery_required"
	}
	return ""
}

func validDiagnostic(code string) bool {
	switch code {
	case "installation_recovery_required", "installation_state_corrupt", "administrator_state_missing", "administrator_state_unavailable":
		return true
	default:
		return false
	}
}

func recoveryRecord() Record {
	return Record{SchemaVersion: SchemaVersion, State: StateRecoveryRequired}
}

func newInstallationID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func readRecord(path string) (Record, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Record{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxRecordSize {
		return Record{}, ErrInvalidState
	}
	if info.Mode().Perm() != 0600 {
		return Record{}, ErrInvalidState
	}
	f, err := os.Open(path)
	if err != nil {
		return Record{}, ErrInvalidState
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return Record{}, ErrInvalidState
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRecordSize+1))
	if err != nil || len(data) == 0 || len(data) > maxRecordSize {
		return Record{}, ErrInvalidState
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, ErrInvalidState
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Record{}, ErrInvalidState
	}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func validateRecord(record Record) error {
	if record.SchemaVersion != SchemaVersion || !installationIDRE.MatchString(record.InstallationID) ||
		(record.State != StateUninitialized && record.State != StateSetupInProgress && record.State != StateReady && record.State != StateRecoveryRequired) ||
		record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return ErrInvalidState
	}
	if record.State == StateReady && (record.ReadyAt.IsZero() || record.ReadyAt.Before(record.CreatedAt)) {
		return ErrInvalidState
	}
	if record.State != StateReady && !record.ReadyAt.IsZero() {
		return ErrInvalidState
	}
	return nil
}

func writeRecord(path string, record Record) (retErr error) {
	if err := validateRecord(record); err != nil {
		return err
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxRecordSize {
		return ErrInvalidState
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".installation-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func ensurePrivateDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidState
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), current) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidState
		}
		if current == path && info.Mode().Perm() != 0700 {
			if err := os.Chmod(current, 0700); err != nil {
				return err
			}
		}
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
