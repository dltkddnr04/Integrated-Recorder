package recordingowner

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testRecording = "0123456789abcdef0123456789abcdef"
	testEngineA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testEngineB   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testWorkerA   = "cccccccccccccccccccccccccccccccc"
	testWorkerB   = "dddddddddddddddddddddddddddddddd"
)

func TestClaimReadAndReopenPersistence(t *testing.T) {
	root := t.TempDir()
	store := mustOpen(t, root)
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Epoch != 1 {
		t.Fatalf("claim epoch=%d, want 1", owner.Epoch)
	}
	if current, err := store.Current(testRecording); err != nil || current != owner {
		t.Fatalf("Current()=%+v, %v; want %+v", current, err, owner)
	}

	reopened := mustOpen(t, root)
	if current, err := reopened.Current(testRecording); err != nil || current != owner {
		t.Fatalf("reopened Current()=%+v, %v; want %+v", current, err, owner)
	}
}

func TestConcurrentClaimsExactlyOneSucceeds(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	const attempts = 24
	var wg sync.WaitGroup
	var successes atomic.Int32
	var unexpected atomic.Int32
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			worker := fmt.Sprintf("%032x", index+1)
			_, err := store.Claim(testRecording, testEngineA, worker)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrAlreadyOwned) {
				unexpected.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful claims=%d, want exactly 1", got)
	}
	if got := unexpected.Load(); got != 0 {
		t.Fatalf("unexpected claim failures=%d", got)
	}
}

func TestTransferFencesOldOwnerAndAcceptsTarget(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	old, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	var oldCommitCalled atomic.Bool
	if err := store.WithCommit(old, func() error { oldCommitCalled.Store(true); return nil }); err != nil {
		t.Fatal(err)
	}
	oldCommitCalled.Store(false)

	newOwner, err := store.Transfer(old, testEngineB, testWorkerB)
	if err != nil {
		t.Fatal(err)
	}
	if newOwner.Epoch != old.Epoch+1 || newOwner.EngineGeneration != testEngineB {
		t.Fatalf("transferred owner=%+v", newOwner)
	}
	if err := store.WithCommit(old, func() error { oldCommitCalled.Store(true); return nil }); !errors.Is(err, ErrStaleOwner) {
		t.Fatalf("stale WithCommit error=%v, want ErrStaleOwner", err)
	}
	if oldCommitCalled.Load() {
		t.Fatal("stale owner commit closure was invoked")
	}
	var targetCommitCalled atomic.Bool
	if err := store.WithCommit(newOwner, func() error { targetCommitCalled.Store(true); return nil }); err != nil {
		t.Fatal(err)
	}
	if !targetCommitCalled.Load() {
		t.Fatal("current owner commit closure was not invoked")
	}
}

func TestReleaseRequiresExactOwner(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	wrong := owner
	wrong.WorkerInstance = testWorkerB
	if err := store.Release(wrong); !errors.Is(err, ErrStaleOwner) {
		t.Fatalf("Release(wrong owner)=%v, want ErrStaleOwner", err)
	}
	if current, err := store.Current(testRecording); err != nil || current != owner {
		t.Fatalf("owner after stale release=%+v, %v", current, err)
	}
	if err := store.Release(owner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Current(testRecording); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Current after release=%v, want ErrNotFound", err)
	}
	if err := store.WithCommit(owner, func() error { t.Fatal("released owner commit callback ran"); return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("released owner commit=%v, want ErrNotFound", err)
	}
	reclaimed, err := store.Claim(testRecording, testEngineB, testWorkerB)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Epoch != owner.Epoch+1 {
		t.Fatalf("claim after release epoch=%d, want %d", reclaimed.Epoch, owner.Epoch+1)
	}
}

func TestCommitClosureNotInvokedForStaleMissingOrInvalidState(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	transferred, err := store.Transfer(owner, testEngineB, testWorkerB)
	if err != nil {
		t.Fatal(err)
	}
	var invoked atomic.Bool
	if err := store.WithCommit(owner, func() error { invoked.Store(true); return nil }); !errors.Is(err, ErrStaleOwner) {
		t.Fatalf("stale commit error=%v", err)
	}
	if err := store.Release(transferred); err != nil {
		t.Fatal(err)
	}
	if err := store.WithCommit(transferred, func() error { invoked.Store(true); return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing commit error=%v", err)
	}
	if invoked.Load() {
		t.Fatal("stale or missing owner invoked commit closure")
	}

	if _, err := store.Claim(testRecording, testEngineA, testWorkerA); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ownerPath(testRecording), []byte(`{"schema_version":99,"recording_id":"`+testRecording+`","engine_generation":"`+testEngineA+`","worker_instance":"`+testWorkerA+`","epoch":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.WithCommit(Owner{RecordingID: testRecording, EngineGeneration: testEngineA, WorkerInstance: testWorkerA, Epoch: 1}, func() error { invoked.Store(true); return nil }); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("malformed commit error=%v, want ErrInvalidState", err)
	}
	if invoked.Load() {
		t.Fatal("malformed owner state invoked commit closure")
	}
}

func TestTransferWaitsForActiveCommitClosure(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	inside := make(chan struct{})
	release := make(chan struct{})
	commitDone := make(chan error, 1)
	go func() {
		commitDone <- store.WithCommit(owner, func() error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside
	transferDone := make(chan error, 1)
	go func() {
		_, err := store.Transfer(owner, testEngineB, testWorkerB)
		transferDone <- err
	}()
	select {
	case err := <-transferDone:
		t.Fatalf("Transfer returned while commit callback held the lock: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	if err := <-commitDone; err != nil {
		t.Fatal(err)
	}
	if err := <-transferDone; err != nil {
		t.Fatal(err)
	}
}

func TestUnownedCommitFencesClaimAndRejectsOwnedArchiveMutation(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	entered := make(chan struct{})
	release := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- store.WithUnownedCommit(testRecording, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	claimDone := make(chan error, 1)
	go func() {
		_, err := store.Claim(testRecording, testEngineA, testWorkerA)
		claimDone <- err
	}()
	select {
	case err := <-claimDone:
		t.Fatalf("Claim completed inside unowned archive mutation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-deleteDone; err != nil {
		t.Fatalf("WithUnownedCommit(): %v", err)
	}
	if err := <-claimDone; err != nil {
		t.Fatalf("Claim after unowned mutation: %v", err)
	}
	var invoked atomic.Bool
	if err := store.WithUnownedCommit(testRecording, func() error { invoked.Store(true); return nil }); !errors.Is(err, ErrAlreadyOwned) {
		t.Fatalf("owned archive mutation error=%v, want ErrAlreadyOwned", err)
	}
	if invoked.Load() {
		t.Fatal("unowned mutation callback ran while a recording owner exists")
	}
}

func TestWithFencedRecoveryClearsOwnersAndFencesStaleCommit(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan error, 1)
	release := make(chan struct{})
	recoveryDone := make(chan error, 1)
	go func() {
		recoveryDone <- store.WithFencedRecovery(func() error {
			_, observedErr := store.readOwner(owner.RecordingID)
			if !errors.Is(observedErr, ErrNotFound) {
				entered <- fmt.Errorf("recovery observed owner=%v, want ErrNotFound", observedErr)
				return fmt.Errorf("recovery observed owner=%v, want ErrNotFound", observedErr)
			}
			entered <- nil
			<-release
			return nil
		})
	}()
	if err := <-entered; err != nil {
		t.Fatal(err)
	}

	commitStarted := make(chan struct{})
	commitDone := make(chan error, 1)
	var commitCalled atomic.Bool
	go func() {
		close(commitStarted)
		commitDone <- store.WithCommit(owner, func() error {
			commitCalled.Store(true)
			return nil
		})
	}()
	<-commitStarted
	select {
	case err := <-commitDone:
		t.Fatalf("stale commit escaped recovery barrier: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	if err := <-recoveryDone; err != nil {
		t.Fatal(err)
	}
	if err := <-commitDone; !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale WithCommit() error=%v, want ErrNotFound", err)
	}
	if commitCalled.Load() {
		t.Fatal("stale commit callback ran after recovery barrier")
	}
}

func TestWithFencedRecoveryFailsClosedOnMalformedOwner(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	if _, err := store.Claim(testRecording, testEngineA, testWorkerA); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ownerPath(testRecording), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	var called atomic.Bool
	err := store.WithFencedRecovery(func() error { called.Store(true); return nil })
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("WithFencedRecovery() error=%v, want ErrInvalidState", err)
	}
	if called.Load() {
		t.Fatal("recovery callback ran despite malformed owner record")
	}
	if _, err := os.Lstat(store.ownerPath(testRecording)); err != nil {
		t.Fatalf("malformed owner record was unexpectedly removed: %v", err)
	}
}

func TestWithFencedRecoveryPropagatesErrorAfterClearingOwners(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	recoveryErr := errors.New("recovery failed")
	err = store.WithFencedRecovery(func() error { return recoveryErr })
	if !errors.Is(err, recoveryErr) {
		t.Fatalf("WithFencedRecovery() error=%v, want callback error", err)
	}
	if _, err := store.Current(owner.RecordingID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Current() after failed recovery=%v, want ErrNotFound", err)
	}
	claimed, err := store.Claim(owner.RecordingID, testEngineB, testWorkerB)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Epoch != owner.Epoch+1 {
		t.Fatalf("claim after failed recovery epoch=%d, want %d", claimed.Epoch, owner.Epoch+1)
	}
}

func TestLegacyOwnerWithoutStateIsActiveAndIdentityIsValidated(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	legacy := []byte(`{"schema_version":1,"recording_id":"` + testRecording + `","engine_generation":"` + testEngineA + `","worker_instance":"` + testWorkerA + `","epoch":9}`)
	writeRawOwner(t, store, testRecording, legacy, 0600)
	want := Owner{RecordingID: testRecording, EngineGeneration: testEngineA, WorkerInstance: testWorkerA, Epoch: 9}
	if got, err := store.Current(testRecording); err != nil || got != want {
		t.Fatalf("legacy Current()=(%+v,%v), want active %+v", got, err, want)
	}
	if err := store.WithCommit(want, func() error { return nil }); err != nil {
		t.Fatalf("legacy owner commit: %v", err)
	}

	badID := []byte(`{"schema_version":1,"recording_id":"` + testRecording + `","engine_generation":"../invalid","worker_instance":"` + testWorkerA + `","epoch":9}`)
	writeRawOwner(t, store, testRecording, badID, 0600)
	if _, err := store.Current(testRecording); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("malformed legacy identity error=%v, want ErrInvalidState", err)
	}
	for _, encodedState := range []string{`"retired"`, `""`, `null`} {
		badState := []byte(`{"schema_version":1,"recording_id":"` + testRecording + `","engine_generation":"` + testEngineA + `","worker_instance":"` + testWorkerA + `","epoch":9,"state":` + encodedState + `}`)
		writeRawOwner(t, store, testRecording, badState, 0600)
		if _, err := store.Current(testRecording); !errors.Is(err, ErrInvalidState) {
			t.Errorf("invalid owner state %s error=%v, want ErrInvalidState", encodedState, err)
		}
	}
}

func TestFencedTombstoneRejectsOldCommitAndClaimIncrementsEpoch(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Release(owner); err != nil {
		t.Fatal(err)
	}
	var called atomic.Bool
	if err := store.WithCommit(owner, func() error { called.Store(true); return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tombstoned owner commit=%v, want ErrNotFound", err)
	}
	if called.Load() {
		t.Fatal("tombstoned owner invoked canonical commit")
	}
	reclaimed, err := store.Claim(testRecording, testEngineB, testWorkerB)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Epoch != 2 {
		t.Fatalf("reclaimed epoch=%d, want 2", reclaimed.Epoch)
	}
	if err := store.WithCommit(owner, func() error { called.Store(true); return nil }); !errors.Is(err, ErrStaleOwner) {
		t.Fatalf("old epoch commit after reclaim=%v, want ErrStaleOwner", err)
	}
	if called.Load() {
		t.Fatal("old epoch invoked canonical commit after reclaim")
	}
}

func TestWithFencedRecoveryIsIdempotentAndPreservesEpochHighWater(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := store.WithFencedRecovery(func() error { return nil }); err != nil {
			t.Fatalf("recovery pass %d: %v", i+1, err)
		}
		if _, err := store.Current(testRecording); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Current after recovery pass %d=%v, want fenced", i+1, err)
		}
		if err := store.WithCommit(owner, func() error { t.Fatal("stale owner committed"); return nil }); !errors.Is(err, ErrNotFound) {
			t.Fatalf("stale commit after recovery pass %d=%v, want ErrNotFound", i+1, err)
		}
	}
	claimed, err := store.Claim(testRecording, testEngineB, testWorkerB)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Epoch != owner.Epoch+1 {
		t.Fatalf("claim after repeated recovery epoch=%d, want %d", claimed.Epoch, owner.Epoch+1)
	}
}

func TestWithFencedRecoveryRemovesCrashTempBeforeFirstOwnerPublication(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	tempPath := createOwnerCrashTemp(t, store, []byte(`{"partial":`))
	if _, err := os.Lstat(store.ownerPath(testRecording)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test setup unexpectedly has an authoritative owner record: %v", err)
	}
	called := false
	if err := store.WithFencedRecovery(func() error {
		called = true
		if _, err := os.Lstat(tempPath); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("crash temp still exists during recovery: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithFencedRecovery with pre-publication temp: %v", err)
	}
	if !called {
		t.Fatal("recovery callback was not called after safe temp cleanup")
	}
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil || owner.Epoch != 1 {
		t.Fatalf("first claim after non-authoritative temp cleanup=(%+v,%v), want epoch 1", owner, err)
	}
}

func TestWithFencedRecoveryRemovesCrashTempBesideAuthoritativeOwner(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	tempPath := createOwnerCrashTemp(t, store, []byte(`{"partial":`))
	if err := store.WithFencedRecovery(func() error { return nil }); err != nil {
		t.Fatalf("WithFencedRecovery with active owner and crash temp: %v", err)
	}
	if _, err := os.Lstat(tempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash temp remained after recovery: %v", err)
	}
	if _, err := store.Current(owner.RecordingID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old owner remained active after recovery: %v", err)
	}
	if err := store.WithCommit(owner, func() error { t.Fatal("fenced old owner committed"); return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old owner commit after recovery=%v, want ErrNotFound", err)
	}
	reclaimed, err := store.Claim(testRecording, testEngineB, testWorkerB)
	if err != nil || reclaimed.Epoch != owner.Epoch+1 {
		t.Fatalf("claim after recovery=(%+v,%v), want epoch %d", reclaimed, err, owner.Epoch+1)
	}
}

func TestWithFencedRecoveryRejectsMaliciousTempAndUnexpectedEntries(t *testing.T) {
	t.Run("recognized name symlink", func(t *testing.T) {
		store := mustOpen(t, t.TempDir())
		target := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(target, []byte("not a temp"), 0600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(store.ownersDir, ".owner-1234567890.tmp")
		if err := os.Symlink(target, path); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		called := false
		if err := store.WithFencedRecovery(func() error { called = true; return nil }); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("recovery with recognized temp symlink=%v, want ErrInvalidState", err)
		}
		if called {
			t.Fatal("recovery callback ran despite suspicious symlink")
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("rejected symlink was removed: %v", err)
		}
	})

	t.Run("unrecognized filename", func(t *testing.T) {
		store := mustOpen(t, t.TempDir())
		path := filepath.Join(store.ownersDir, ".owner-suspicious.tmp")
		if err := os.WriteFile(path, []byte("partial"), 0600); err != nil {
			t.Fatal(err)
		}
		called := false
		if err := store.WithFencedRecovery(func() error { called = true; return nil }); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("recovery with unrecognized entry=%v, want ErrInvalidState", err)
		}
		if called {
			t.Fatal("recovery callback ran despite unrecognized entry")
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("rejected unexpected entry was removed: %v", err)
		}
	})
}

func createOwnerCrashTemp(t *testing.T, store *Store, data []byte) string {
	t.Helper()
	temp, err := os.CreateTemp(store.ownersDir, ".owner-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	path := temp.Name()
	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		t.Fatal(err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		t.Fatal(err)
	}
	if err := temp.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOwnerTransitionWaitsForFencedRecovery(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	recordingID := differentRecordingID(t, testRecording)
	entered := make(chan struct{})
	release := make(chan struct{})
	recoveryDone := make(chan error, 1)
	go func() {
		recoveryDone <- store.WithFencedRecovery(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	transitionStarted := make(chan struct{})
	transitionDone := make(chan error, 1)
	go func() {
		close(transitionStarted)
		_, err := store.Claim(recordingID, testEngineB, testWorkerB)
		transitionDone <- err
	}()
	<-transitionStarted
	select {
	case err := <-transitionDone:
		t.Fatalf("Claim escaped recovery barrier: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	if err := <-recoveryDone; err != nil {
		t.Fatal(err)
	}
	if err := <-transitionDone; err != nil {
		t.Fatalf("Claim after recovery barrier=%v, want success", err)
	}
}

func differentRecordingID(t *testing.T, other string) string {
	t.Helper()
	for i := uint64(1); i < 1<<20; i++ {
		candidate := fmt.Sprintf("%032x", i)
		if candidate != other && lockShardFor(candidate) != lockShardFor(other) {
			return candidate
		}
	}
	t.Fatal("could not find recording ID on a different lock shard")
	return ""
}

func TestCrossProcessTransferWaitsForCommitClosure(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("OS flock process test is supported on Darwin and Linux")
	}
	root := t.TempDir()
	store := mustOpen(t, root)
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	releasePath := filepath.Join(root, "release-commit")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSubprocessCommitLockHelper$")
	cmd.Env = append(os.Environ(),
		"RECORDING_OWNER_HELPER=1",
		"RECORDING_OWNER_DATA_DIR="+root,
		"RECORDING_OWNER_RELEASE_PATH="+releasePath,
		"RECORDING_OWNER_RECORDING_ID="+owner.RecordingID,
		"RECORDING_OWNER_ENGINE_ID="+owner.EngineGeneration,
		"RECORDING_OWNER_WORKER_ID="+owner.WorkerInstance,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line := make(chan string, 1)
	go func() { text, _ := bufio.NewReader(stdout).ReadString('\n'); line <- text }()
	select {
	case text := <-line:
		if strings.TrimSpace(text) != "commit-lock-held" {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("helper readiness line=%q", text)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("subprocess did not acquire the recording commit lock")
	}

	transferDone := make(chan error, 1)
	go func() { _, err := store.Transfer(owner, testEngineB, testWorkerB); transferDone <- err }()
	select {
	case err := <-transferDone:
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("cross-process Transfer returned while commit lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.WriteFile(releasePath, []byte("ok"), 0600); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("commit-lock helper exited with error: %v", err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		select {
		case <-waitDone:
		case <-time.After(time.Second):
		}
		select {
		case <-transferDone:
		case <-time.After(time.Second):
		}
		t.Fatal("commit-lock helper did not exit after release")
	}
	if err := <-transferDone; err != nil {
		t.Fatal(err)
	}
}

func TestSubprocessCommitLockHelper(t *testing.T) {
	if os.Getenv("RECORDING_OWNER_HELPER") != "1" {
		return
	}
	store, err := Open(os.Getenv("RECORDING_OWNER_DATA_DIR"))
	if err != nil {
		t.Fatal(err)
	}
	owner := Owner{
		RecordingID:      os.Getenv("RECORDING_OWNER_RECORDING_ID"),
		EngineGeneration: os.Getenv("RECORDING_OWNER_ENGINE_ID"),
		WorkerInstance:   os.Getenv("RECORDING_OWNER_WORKER_ID"),
		Epoch:            1,
	}
	err = store.WithCommit(owner, func() error {
		fmt.Fprintln(os.Stdout, "commit-lock-held")
		releasePath := os.Getenv("RECORDING_OWNER_RELEASE_PATH")
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(releasePath); err == nil {
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		return errors.New("test parent did not release commit lock")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Skip("helper process complete")
}

func TestRejectsCorruptUnknownSymlinkNonRegularAndInvalidRecords(t *testing.T) {
	t.Run("corrupt json", func(t *testing.T) {
		store := mustOpen(t, t.TempDir())
		writeRawOwner(t, store, testRecording, []byte("{"), 0600)
		if _, err := store.Current(testRecording); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Current() error=%v, want ErrInvalidState", err)
		}
	})
	t.Run("unknown schema", func(t *testing.T) {
		store := mustOpen(t, t.TempDir())
		writeRawOwner(t, store, testRecording, []byte(`{"schema_version":2,"recording_id":"`+testRecording+`","engine_generation":"`+testEngineA+`","worker_instance":"`+testWorkerA+`","epoch":1}`), 0600)
		if _, err := store.Current(testRecording); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Current() error=%v, want ErrInvalidState", err)
		}
	})
	t.Run("symlink owner", func(t *testing.T) {
		store := mustOpen(t, t.TempDir())
		target := filepath.Join(t.TempDir(), "owner.json")
		if err := os.WriteFile(target, []byte(`{"schema_version":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, store.ownerPath(testRecording)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := store.Current(testRecording); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Current() error=%v, want ErrInvalidState", err)
		}
	})
	t.Run("nonregular owner", func(t *testing.T) {
		store := mustOpen(t, t.TempDir())
		if err := os.Mkdir(store.ownerPath(testRecording), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Current(testRecording); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("Current() error=%v, want ErrInvalidState", err)
		}
	})
	t.Run("invalid id", func(t *testing.T) {
		store := mustOpen(t, t.TempDir())
		for _, id := range []string{"../escape", strings.Repeat("a", 31), strings.Repeat("A", 32), ""} {
			if _, err := store.Current(id); !errors.Is(err, ErrInvalidIdentity) {
				t.Errorf("Current(%q) error=%v, want ErrInvalidIdentity", id, err)
			}
		}
		if _, err := store.Claim(testRecording, "../generation", testWorkerA); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("Claim invalid generation error=%v", err)
		}
	})
}

func TestRejectsSymlinkAndNonRegularLockFiles(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		locksDir := filepath.Join(root, "runtime", ownerDirName, lockDirName)
		if err := os.MkdirAll(locksDir, 0700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "outside-lock")
		if err := os.WriteFile(target, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(locksDir, shardName(0))); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if _, err := Open(root); err == nil {
			t.Fatal("Open accepted a symlink lock file")
		}
	})
	t.Run("nonregular", func(t *testing.T) {
		root := t.TempDir()
		locksDir := filepath.Join(root, "runtime", ownerDirName, lockDirName)
		if err := os.MkdirAll(locksDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(locksDir, shardName(0)), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(root); err == nil {
			t.Fatal("Open accepted a non-regular lock file")
		}
	})
}

func TestPrivatePermissions(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "runtime"), 0755); err != nil {
		t.Fatal(err)
	}
	store := mustOpen(t, root)
	for _, path := range []string{
		filepath.Join(root, "runtime"),
		store.ownersDir,
		store.locksDir,
		filepath.Join(store.locksDir, shardName(0)),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0700)
		if strings.HasPrefix(filepath.Base(path), ".lock-") {
			want = 0600
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode=%#o, want %#o", path, got, want)
		}
	}
	if _, err := store.Claim(testRecording, testEngineA, testWorkerA); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.ownerPath(testRecording))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("owner mode=%#o, want 0600", info.Mode().Perm())
	}
}

func TestRejectsOwnerWithWrongPermissions(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.ownerPath(testRecording), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Current(owner.RecordingID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Current() error=%v, want ErrInvalidState", err)
	}
}

func TestTransferRejectsEpochOverflow(t *testing.T) {
	store := mustOpen(t, t.TempDir())
	owner, err := store.Claim(testRecording, testEngineA, testWorkerA)
	if err != nil {
		t.Fatal(err)
	}
	owner.Epoch = ^uint64(0)
	data, err := json.Marshal(ownerRecord{SchemaVersion: SchemaVersion, RecordingID: owner.RecordingID, EngineGeneration: owner.EngineGeneration, WorkerInstance: owner.WorkerInstance, Epoch: owner.Epoch})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.ownerPath(testRecording), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transfer(owner, testEngineB, testWorkerB); !errors.Is(err, ErrEpochOverflow) {
		t.Fatalf("Transfer() error=%v, want ErrEpochOverflow", err)
	}
}

func writeRawOwner(t *testing.T, store *Store, recordingID string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(store.ownerPath(recordingID), data, mode); err != nil {
		t.Fatal(err)
	}
}

func mustOpen(t *testing.T, root string) *Store {
	t.Helper()
	store, err := Open(root)
	if err != nil {
		if errors.Is(err, ErrLockUnsupported) {
			t.Skipf("safe cross-process locking is unsupported: %v", err)
		}
		t.Fatal(err)
	}
	return store
}
