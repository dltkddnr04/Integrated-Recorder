// Package recordingowner stores Runtime Host-owned, durable fencing records
// for canonical Recording writers. The per-recording OS lock is shared by
// owner transitions and complete canonical commit closures, making the owner
// check and the write mutually exclusive across processes.
package recordingowner

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	SchemaVersion = 1
	ownerDirName  = "recording-owners"
	lockDirName   = "locks"
	lockShards    = 256
	maxRecordSize = 4 << 10
)

var (
	ErrNotFound        = errors.New("recording owner not found")
	ErrAlreadyOwned    = errors.New("recording already has an owner")
	ErrStaleOwner      = errors.New("recording owner does not match")
	ErrInvalidIdentity = errors.New("recording owner identity is invalid")
	ErrInvalidState    = errors.New("recording owner state is invalid")
	ErrLockUnsupported = errors.New("safe cross-process file locking is unsupported")
	ErrEpochOverflow   = errors.New("recording ownership epoch is exhausted")
	ErrInvalidCommit   = errors.New("recording commit callback is nil")
	ErrInvalidRecovery = errors.New("recording recovery callback is nil")

	recordingIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	identityPattern    = regexp.MustCompile(`^(?:[0-9a-fA-F]{32,64}|[0-9a-fA-F]{8}-(?:[0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12})$`)
	// CreateTemp renders its random uint32 component as 1-10 decimal digits.
	// Keep cleanup scoped to the exact owner writer prefix/suffix and that
	// bounded generated component.
	ownerTempNamePattern = regexp.MustCompile(`^\.owner-[0-9]{1,10}\.tmp$`)
)

// Owner is the Host-authorized canonical writer identity for one Recording.
// Epoch increases exactly once on every ownership transfer.
type Owner struct {
	RecordingID      string `json:"recording_id"`
	EngineGeneration string `json:"engine_generation"`
	WorkerInstance   string `json:"worker_instance"`
	Epoch            uint64 `json:"epoch"`
}

// Store is a Host-owned durable owner registry. It contains no Engine-local
// state and does not itself decide which generation should own a Recording.
type Store struct {
	dataDir   string
	ownersDir string
	locksDir  string
}

type ownerRecord struct {
	SchemaVersion    int    `json:"schema_version"`
	RecordingID      string `json:"recording_id"`
	EngineGeneration string `json:"engine_generation"`
	WorkerInstance   string `json:"worker_instance"`
	Epoch            uint64 `json:"epoch"`
	// State was added without changing the schema version. Older records that
	// omit it are active owners. Fenced records are durable epoch high-water
	// tombstones and may never authorize a canonical commit.
	State ownerState `json:"state,omitempty"`
}

type ownerState string

const (
	ownerActive ownerState = "active"
	ownerFenced ownerState = "fenced"
)

// Open initializes the private owner store and a bounded, fixed set of lock
// shards. Lock shards are never removed: process death releases the OS lock,
// while deleting/recreating a lock file could split coordination across two
// inodes.
func Open(dataDir string) (*Store, error) {
	if !lockingSupported() {
		return nil, ErrLockUnsupported
	}
	if strings.TrimSpace(dataDir) == "" {
		return nil, ErrInvalidIdentity
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil || filepath.Clean(abs) != abs {
		return nil, ErrInvalidIdentity
	}
	runtimeDir := filepath.Join(abs, "runtime")
	ownersDir := filepath.Join(runtimeDir, ownerDirName)
	locksDir := filepath.Join(ownersDir, lockDirName)
	for _, dir := range []string{runtimeDir, ownersDir, locksDir} {
		if err := ensurePrivateDir(dir); err != nil {
			return nil, fmt.Errorf("recording owner store directory is unavailable: %w", err)
		}
	}
	// Persist the nested directory entries before relying on the owner file's
	// later directory sync to make ownership transitions durable.
	for _, dir := range []string{abs, runtimeDir, ownersDir} {
		if err := syncDirectory(dir); err != nil {
			return nil, fmt.Errorf("recording owner store parent directory could not be synchronized: %w", err)
		}
	}
	s := &Store{dataDir: abs, ownersDir: ownersDir, locksDir: locksDir}
	for i := 0; i < lockShards; i++ {
		f, err := openLockShard(filepath.Join(locksDir, shardName(i)))
		if err != nil {
			return nil, fmt.Errorf("recording owner lock shard is invalid: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("recording owner lock shard could not be closed: %w", err)
		}
	}
	if err := syncDirectory(locksDir); err != nil {
		return nil, fmt.Errorf("recording owner lock directory could not be synchronized: %w", err)
	}
	return s, nil
}

// Claim creates the first owner for a Recording at epoch 1. A durable fenced
// tombstone is retained as an epoch high-water mark, so reusing an archive ID
// can never make a stale worker token valid again.
func (s *Store) Claim(recordingID, generationID, workerInstance string) (Owner, error) {
	if s == nil || !validOwnerIdentity(Owner{RecordingID: recordingID, EngineGeneration: generationID, WorkerInstance: workerInstance, Epoch: 1}) {
		return Owner{}, ErrInvalidIdentity
	}
	var result Owner
	err := s.withIDLock(recordingID, func() error {
		record, err := s.readOwnerRecord(recordingID)
		switch {
		case err == nil:
			if effectiveOwnerState(record.State) == ownerActive {
				return ErrAlreadyOwned
			}
			if record.Epoch == ^uint64(0) {
				return ErrEpochOverflow
			}
			result = Owner{RecordingID: recordingID, EngineGeneration: generationID, WorkerInstance: workerInstance, Epoch: record.Epoch + 1}
		case errors.Is(err, ErrNotFound):
			result = Owner{RecordingID: recordingID, EngineGeneration: generationID, WorkerInstance: workerInstance, Epoch: 1}
		default:
			return err
		}
		return s.writeOwner(result)
	})
	if err != nil {
		return Owner{}, err
	}
	return result, nil
}

// Transfer atomically replaces an exact current owner with the target owner
// and increments the epoch by one. A stale caller cannot take ownership.
func (s *Store) Transfer(expected Owner, targetGeneration, targetInstance string) (Owner, error) {
	if s == nil || !validOwnerIdentity(expected) || !validIdentity(targetGeneration) || !validIdentity(targetInstance) {
		return Owner{}, ErrInvalidIdentity
	}
	var result Owner
	err := s.withIDLock(expected.RecordingID, func() error {
		current, err := s.readOwner(expected.RecordingID)
		if err != nil {
			return err
		}
		if current != expected {
			return ErrStaleOwner
		}
		if current.Epoch == ^uint64(0) {
			return ErrEpochOverflow
		}
		result = Owner{
			RecordingID:      current.RecordingID,
			EngineGeneration: targetGeneration,
			WorkerInstance:   targetInstance,
			Epoch:            current.Epoch + 1,
		}
		return s.writeOwner(result)
	})
	if err != nil {
		return Owner{}, err
	}
	return result, nil
}

// Release durably fences an owner only when the complete expected tuple is
// still current. The tombstone retains the epoch high-water mark.
func (s *Store) Release(expected Owner) error {
	if s == nil || !validOwnerIdentity(expected) {
		return ErrInvalidIdentity
	}
	return s.withIDLock(expected.RecordingID, func() error {
		current, err := s.readOwner(expected.RecordingID)
		if err != nil {
			return err
		}
		if current != expected {
			return ErrStaleOwner
		}
		return s.writeOwnerRecord(current, ownerFenced)
	})
}

// Current returns the current durable owner. It takes the same shard lock as
// transitions and canonical writes so callers never observe a partial update.
func (s *Store) Current(recordingID string) (Owner, error) {
	if s == nil || !validRecordingID(recordingID) {
		return Owner{}, ErrInvalidIdentity
	}
	var result Owner
	err := s.withIDLock(recordingID, func() error {
		var err error
		result, err = s.readOwner(recordingID)
		return err
	})
	if err != nil {
		return Owner{}, err
	}
	return result, nil
}

// WithCommit holds the recording's cross-process lock from exact owner
// validation through the entire canonical write callback. Host owner changes
// use this same lock. The callback must not call Store owner methods for this
// Recording ID.
func (s *Store) WithCommit(expected Owner, commit func() error) error {
	if s == nil || !validOwnerIdentity(expected) {
		return ErrInvalidIdentity
	}
	if commit == nil {
		return ErrInvalidCommit
	}
	return s.withIDLock(expected.RecordingID, func() error {
		current, err := s.readOwner(expected.RecordingID)
		if err != nil {
			return err
		}
		if current != expected {
			return ErrStaleOwner
		}
		return commit()
	})
}

// WithUnownedCommit serializes an inactive archive mutation against owner
// claims, transfers, and canonical commits. It runs only while the durable
// owner record is absent, so deletion cannot race a newly admitted worker.
func (s *Store) WithUnownedCommit(recordingID string, commit func() error) error {
	if s == nil || !recordingIDPattern.MatchString(recordingID) {
		return ErrInvalidIdentity
	}
	if commit == nil {
		return ErrInvalidCommit
	}
	return s.withIDLock(recordingID, func() error {
		record, err := s.readOwnerRecord(recordingID)
		if errors.Is(err, ErrNotFound) {
			return commit()
		}
		if err != nil {
			return err
		}
		if effectiveOwnerState(record.State) == ownerFenced {
			return commit()
		}
		return ErrAlreadyOwned
	})
}

// WithFencedRecovery is the managed-startup gate for mutating archive
// recovery. It takes every owner lock shard in ascending order, validates the
// complete owner directory, durably tombstones every active owner, and only
// then invokes recover. The locks remain held for the callback's full
// duration, so no old commit token or concurrent owner transition can become
// valid while recovery is mutating canonical archives. Fenced records remain
// as durable epoch high-water tombstones.
//
// The callback must not call Store methods: all shards are held until it
// returns. Recovery errors are returned to the caller; all owners have
// already been tombstoned, making a subsequent recovery retry idempotent.
// Callers must fail startup if this method returns an error.
func (s *Store) WithFencedRecovery(recover func() error) error {
	if s == nil {
		return ErrInvalidIdentity
	}
	if recover == nil {
		return ErrInvalidRecovery
	}
	return s.withAllLocks(func() error {
		if err := s.reconcileOwnerTempFiles(); err != nil {
			return err
		}
		owners, err := s.validatedOwnerFiles()
		if err != nil {
			return err
		}
		for _, owner := range owners {
			path := s.ownerPath(owner.id)
			current, err := os.Lstat(path)
			if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(owner.info, current) {
				return ErrInvalidState
			}
			if effectiveOwnerState(owner.record.State) == ownerFenced {
				continue
			}
			if err := s.writeOwnerRecord(owner.record.owner(), ownerFenced); err != nil {
				return fmt.Errorf("recording owner recovery fence could not be synchronized: %w", err)
			}
		}
		return recover()
	})
}

// reconcileOwnerTempFiles removes only crash remnants created by
// writeOwnerRecord's exact CreateTemp pattern. These files are never
// authoritative: only an atomically published .json record participates in
// the owner CAS. The caller holds every owner lock shard, so no cooperating
// writer can be midway through a publication. Any suspicious directory entry
// fails closed before owner recovery proceeds.
func (s *Store) reconcileOwnerTempFiles() error {
	entries, err := os.ReadDir(s.ownersDir)
	if err != nil {
		return err
	}
	type tempFile struct {
		path string
		info os.FileInfo
	}
	var remnants []tempFile
	lockDirSeen := false
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(s.ownersDir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return ErrInvalidState
		}
		if name == lockDirName {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return ErrInvalidState
			}
			lockDirSeen = true
			continue
		}
		if strings.HasSuffix(name, ".json") {
			// Authoritative owner records receive their full strict validation
			// immediately after temp reconciliation.
			continue
		}
		if !ownerTempNamePattern.MatchString(name) || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			info.Mode().Perm() != 0600 || info.Size() < 0 || info.Size() > maxRecordSize {
			return ErrInvalidState
		}
		f, err := openOwnerRead(path)
		if err != nil {
			return ErrInvalidState
		}
		opened, statErr := f.Stat()
		closeErr := f.Close()
		if statErr != nil || closeErr != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0600 ||
			opened.Size() < 0 || opened.Size() > maxRecordSize || !os.SameFile(info, opened) {
			return ErrInvalidState
		}
		remnants = append(remnants, tempFile{path: path, info: info})
	}
	if !lockDirSeen {
		return ErrInvalidState
	}
	if len(remnants) == 0 {
		return nil
	}
	for _, remnant := range remnants {
		current, err := os.Lstat(remnant.path)
		if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
			current.Mode().Perm() != 0600 || current.Size() < 0 || current.Size() > maxRecordSize ||
			!os.SameFile(remnant.info, current) {
			return ErrInvalidState
		}
		if err := os.Remove(remnant.path); err != nil {
			return fmt.Errorf("recording owner temp residue could not be removed: %w", err)
		}
	}
	if err := syncDirectory(s.ownersDir); err != nil {
		return fmt.Errorf("recording owner temp cleanup could not be synchronized: %w", err)
	}
	return nil
}

type ownerFile struct {
	id     string
	info   os.FileInfo
	record ownerRecord
}

// validatedOwnerFiles rejects every owner-directory entry except the fixed
// lock directory and well-formed owner records. In particular, stale temp
// files, unknown names, symlinks, and non-regular entries fail closed rather
// than being silently ignored during a recovery fence.
func (s *Store) validatedOwnerFiles() ([]ownerFile, error) {
	entries, err := os.ReadDir(s.ownersDir)
	if err != nil {
		return nil, err
	}
	owners := make([]ownerFile, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(s.ownersDir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, ErrInvalidState
		}
		if name == lockDirName {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, ErrInvalidState
			}
			continue
		}
		if !strings.HasSuffix(name, ".json") {
			return nil, ErrInvalidState
		}
		id := strings.TrimSuffix(name, ".json")
		if !validRecordingID(id) || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrInvalidState
		}
		record, err := s.readOwnerRecord(id)
		if err != nil {
			return nil, err
		}
		owners = append(owners, ownerFile{id: id, info: info, record: record})
	}
	return owners, nil
}

// withAllLocks acquires every fixed shard in deterministic order. The lock
// set is bounded and shard files are never replaced, so this composes safely
// with both one-shard commit operations and other recovery barriers.
func (s *Store) withAllLocks(fn func() error) error {
	files := make([]*os.File, 0, lockShards)
	defer func() {
		for i := len(files) - 1; i >= 0; i-- {
			unlockFile(files[i])
			_ = files[i].Close()
		}
	}()
	for i := 0; i < lockShards; i++ {
		f, err := openLockShard(filepath.Join(s.locksDir, shardName(i)))
		if err != nil {
			return fmt.Errorf("recording owner recovery lock is unavailable: %w", err)
		}
		files = append(files, f)
		if err := lockFile(f); err != nil {
			return fmt.Errorf("recording owner recovery lock could not be acquired: %w", err)
		}
	}
	return fn()
}

func (s *Store) withIDLock(recordingID string, fn func() error) error {
	if s == nil || !validRecordingID(recordingID) {
		return ErrInvalidIdentity
	}
	f, err := openLockShard(filepath.Join(s.locksDir, shardName(lockShardFor(recordingID))))
	if err != nil {
		return fmt.Errorf("recording owner lock is unavailable: %w", err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return fmt.Errorf("recording owner lock could not be acquired: %w", err)
	}
	defer unlockFile(f)
	return fn()
}

func (s *Store) ownerPath(recordingID string) string {
	return filepath.Join(s.ownersDir, recordingID+".json")
}

func (s *Store) readOwner(recordingID string) (Owner, error) {
	record, err := s.readOwnerRecord(recordingID)
	if err != nil {
		return Owner{}, err
	}
	if effectiveOwnerState(record.State) != ownerActive {
		return Owner{}, ErrNotFound
	}
	return record.owner(), nil
}

func (s *Store) readOwnerRecord(recordingID string) (ownerRecord, error) {
	path := s.ownerPath(recordingID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ownerRecord{}, ErrNotFound
	}
	if err != nil {
		return ownerRecord{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxRecordSize || info.Mode().Perm() != 0600 {
		return ownerRecord{}, ErrInvalidState
	}
	f, err := openOwnerRead(path)
	if err != nil {
		return ownerRecord{}, ErrInvalidState
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return ownerRecord{}, ErrInvalidState
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRecordSize+1))
	if err != nil || len(data) == 0 || len(data) > maxRecordSize {
		return ownerRecord{}, ErrInvalidState
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record ownerRecord
	if err := decoder.Decode(&record); err != nil {
		return ownerRecord{}, ErrInvalidState
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ownerRecord{}, ErrInvalidState
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return ownerRecord{}, ErrInvalidState
	}
	_, stateWasEncoded := fields["state"]
	owner := record.owner()
	if record.SchemaVersion != SchemaVersion || record.RecordingID != recordingID || !validOwnerIdentity(owner) ||
		!validOwnerState(record.State) || (stateWasEncoded && record.State != ownerActive && record.State != ownerFenced) {
		return ownerRecord{}, ErrInvalidState
	}
	return record, nil
}

func (s *Store) writeOwner(owner Owner) error {
	return s.writeOwnerRecord(owner, ownerActive)
}

func (s *Store) writeOwnerRecord(owner Owner, state ownerState) error {
	if !validOwnerIdentity(owner) {
		return ErrInvalidIdentity
	}
	if !validOwnerState(state) {
		return ErrInvalidState
	}
	record := ownerRecord{
		SchemaVersion: SchemaVersion, RecordingID: owner.RecordingID,
		EngineGeneration: owner.EngineGeneration, WorkerInstance: owner.WorkerInstance,
		Epoch: owner.Epoch, State: state,
	}
	data, err := json.Marshal(record)
	if err != nil || len(data)+1 > maxRecordSize {
		return ErrInvalidState
	}
	tmp, err := os.CreateTemp(s.ownersDir, ".owner-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
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
	if err := os.Rename(tmpPath, s.ownerPath(owner.RecordingID)); err != nil {
		return err
	}
	cleanup = false
	return syncDirectory(s.ownersDir)
}

func (record ownerRecord) owner() Owner {
	return Owner{RecordingID: record.RecordingID, EngineGeneration: record.EngineGeneration, WorkerInstance: record.WorkerInstance, Epoch: record.Epoch}
}

func effectiveOwnerState(state ownerState) ownerState {
	if state == "" {
		return ownerActive
	}
	return state
}

func validOwnerState(state ownerState) bool {
	return state == "" || state == ownerActive || state == ownerFenced
}

func validOwnerIdentity(owner Owner) bool {
	return validRecordingID(owner.RecordingID) && validIdentity(owner.EngineGeneration) && validIdentity(owner.WorkerInstance) && owner.Epoch > 0
}

func validRecordingID(id string) bool { return recordingIDPattern.MatchString(id) }

func validIdentity(id string) bool { return identityPattern.MatchString(id) }

func lockShardFor(recordingID string) int {
	hash := sha256.Sum256([]byte(recordingID))
	return int(hash[0])
}

func shardName(index int) string { return fmt.Sprintf(".lock-%03d", index) }

func ensurePrivateDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrInvalidIdentity
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidState
	}
	if info.Mode().Perm() != 0700 {
		if err := os.Chmod(path, 0700); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
