package resources

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeResourceIPCOverPrivateUnixSocket(t *testing.T) {
	coordinator := testCoordinator(t, 32, 24, 2)
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "runtime-resources-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "resources.sock")
	server, err := NewIPCServer(socket, token[:], coordinator)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("resource IPC server shutdown: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("resource IPC server did not stop")
		}
	})

	client, err := NewRuntimeClient(socket, token[:], "engine-a")
	if err != nil {
		t.Fatal(err)
	}
	const recordingID = "0123456789abcdef0123456789abcdef"
	if err := client.SetReservation(context.Background(), recordingID, "payload-a", 12); err != nil {
		t.Fatal(err)
	}
	if err := client.AcquireQueue(context.Background(), "job-a"); err != nil {
		t.Fatal(err)
	}
	if err := client.AcquireWriter(context.Background(), "writer-a"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.UsedBytes != 12 || snapshot.QueueObjects != 1 || snapshot.ActiveWriters != 1 {
		t.Fatalf("aggregate snapshot = %+v", snapshot)
	}
	if err := client.ReportProcessTelemetry(context.Background(), 100, 250, 2, 9, 1.25); err != nil {
		t.Fatalf("report telemetry: %v", err)
	}
	if err := client.ReportProcessTelemetry(context.Background(), 100, 250, 2, 9, 1.25); err != nil {
		t.Fatalf("repeat telemetry report: %v", err)
	}
	coordinator.mu.Lock()
	_, hasBoundOwner := coordinator.telemetryOwners["engine-a"]
	_, hasUnexpectedOwner := coordinator.telemetryOwners["engine-b"]
	coordinator.mu.Unlock()
	if !hasBoundOwner || hasUnexpectedOwner {
		t.Fatalf("telemetry report was not bound to RuntimeClient owner: owners=%v", coordinator.telemetryOwners)
	}
	globalSnapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if globalSnapshot.QueueBytes != 9 || globalSnapshot.OldestAgeSeconds != 1.25 {
		t.Fatalf("IPC queue gauge projection = %+v", globalSnapshot)
	}
	telemetry, err := client.TelemetrySnapshot(context.Background())
	if err != nil {
		t.Fatalf("telemetry snapshot: %v", err)
	}
	if telemetry.Throughput.ReadBytesTotal != 100 || telemetry.Throughput.WriteBytesTotal != 250 || telemetry.ErrorsTotal != 2 {
		t.Fatalf("telemetry report was not bound/idempotent: %+v", telemetry)
	}
	if err := client.ReleaseWriter(context.Background(), "writer-a"); err != nil {
		t.Fatal(err)
	}
	if err := client.ReleaseQueue(context.Background(), "job-a"); err != nil {
		t.Fatal(err)
	}
	if err := client.ReleaseReservation(context.Background(), recordingID, "payload-a"); err != nil {
		t.Fatal(err)
	}
	if got := coordinator.Snapshot(); got.UsedBytes != 0 || got.QueueObjects != 0 || got.ActiveWriters != 0 {
		t.Fatalf("released leases remain: %+v", got)
	}
}

func TestRuntimeResourceIPCRejectsInvalidAndUnboundedOperations(t *testing.T) {
	handler, err := NewIPCHandler(testCoordinator(t, 32, 24, 2))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		operation string
		payload   string
	}{
		{operation: operationSetReservation, payload: `{"owner_id":"engine-a","recording_id":"../bad","reservation_id":"lease-a","desired_bytes":1}`},
		{operation: operationSetReservation, payload: `{"owner_id":"engine-a","recording_id":"0123456789abcdef0123456789abcdef","reservation_id":"lease-a","desired_bytes":0}`},
		{operation: operationAcquireQueue, payload: `{"owner_id":"engine-a","lease_id":"bad/id"}`},
		{operation: operationAcquireQueue, payload: `{"owner_id":"engine-a","lease_id":"lease-a","extra":true}`},
		{operation: operationReportTelemetry, payload: `{"read_bytes":1,"write_bytes":2,"errors":0}`},
		{operation: operationReportTelemetry, payload: `{"owner_id":"engine-a","read_bytes":1,"write_bytes":2,"errors":0,"recording_id":"0123456789abcdef0123456789abcdef"}`},
		{operation: operationReportTelemetry, payload: `{"owner_id":"engine-a","read_bytes":1,"write_bytes":2,"errors":0,"queue_bytes":-1}`},
		{operation: "resource_release_owner", payload: `{"owner_id":"engine-a"}`},
	}
	for _, test := range cases {
		t.Run(test.operation+test.payload, func(t *testing.T) {
			if _, err := handler.Handle(context.Background(), test.operation, []byte(test.payload)); err == nil {
				t.Fatal("invalid resource operation unexpectedly succeeded")
			}
		})
	}
	if _, err := handler.Handle(context.Background(), operationSnapshot, []byte(`{"recording_id":"0123456789abcdef0123456789abcdef"}`)); err == nil {
		t.Fatal("snapshot unexpectedly accepted an identity payload")
	}
}

func TestRuntimeResourceOwnerDeathCleanupReclaimsAllLeaseKinds(t *testing.T) {
	c := testCoordinator(t, 64, 48, 2)
	ctx := context.Background()
	if err := c.SetReservation(ctx, "engine-a", "0123456789abcdef0123456789abcdef", "payload-a", 16); err != nil {
		t.Fatal(err)
	}
	if err := c.AcquireQueue(ctx, "engine-a", "job-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.AcquireWriter(ctx, "engine-a", "writer-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.ReleaseOwner("engine-a"); err != nil {
		t.Fatal(err)
	}
	if got := c.Snapshot(); got.UsedBytes != 0 || got.QueueObjects != 0 || got.ActiveWriters != 0 {
		t.Fatalf("owner death did not reclaim leases: %+v", got)
	}
	if err := c.ReleaseOwner("engine-a"); err != nil {
		t.Fatalf("owner cleanup must be idempotent: %v", err)
	}
	if err := c.ReleaseReservation("engine-a", "0123456789abcdef0123456789abcdef", "payload-a"); err != nil {
		t.Fatalf("already absent lease release error = %v", err)
	}
}

func TestRuntimeResourceIPCRejectsWrongToken(t *testing.T) {
	c := testCoordinator(t, 32, 24, 2)
	var token, wrong [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(wrong[:]); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "runtime-resources-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "resources.sock")
	server, err := NewIPCServer(socket, token[:], c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	client, err := NewRuntimeClient(socket, wrong[:], "engine-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AcquireQueue(context.Background(), "job-a"); err == nil {
		t.Fatal("client with wrong Host token was accepted")
	}
	if snapshot := c.Snapshot(); snapshot.QueueObjects != 0 {
		t.Fatalf("unauthorized client changed coordinator state: %+v", snapshot)
	}
}
