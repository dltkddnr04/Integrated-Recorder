package storageproto

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

type ConformanceCheck struct {
	Name string `json:"name"`
	Pass bool   `json:"pass"`
}

type ConformanceReport struct {
	Passed          bool               `json:"passed"`
	ProtocolVersion int                `json:"protocol_version"`
	ProviderID      string             `json:"provider_id,omitempty"`
	ProviderVersion string             `json:"provider_version,omitempty"`
	Fingerprint     string             `json:"descriptor_fingerprint,omitempty"`
	Checks          []ConformanceCheck `json:"checks"`
}

type ConformanceError struct{ Check string }

func (e *ConformanceError) Error() string { return "storage provider conformance failed: " + e.Check }

// RunConformance starts the executable as an isolated child and exercises its
// wire contract over the authenticated private Unix socket. The provider
// implementation is never linked into this runner.
func RunConformance(ctx context.Context, binary string, supplied *Config) (ConformanceReport, error) {
	report := ConformanceReport{ProtocolVersion: Version, Checks: []ConformanceCheck{}}
	if ctx == nil || !filepath.IsAbs(binary) {
		return report, &ConformanceError{Check: "launch_config"}
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
	}
	workspace, err := os.MkdirTemp("/tmp", "ir-spv1-")
	if err != nil {
		return report, &ConformanceError{Check: "workspace"}
	}
	defer os.RemoveAll(workspace)
	_ = os.Chmod(workspace, 0700)
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return report, &ConformanceError{Check: "workspace"}
	}
	var randomToken [32]byte
	if _, err = rand.Read(randomToken[:]); err != nil {
		return report, &ConformanceError{Check: "credential"}
	}
	tokenPath := filepath.Join(workspace, "token")
	if err = os.WriteFile(tokenPath, []byte(hex.EncodeToString(randomToken[:])), 0600); err != nil {
		return report, &ConformanceError{Check: "credential"}
	}
	for i := range randomToken {
		randomToken[i] = 0
	}
	socketPath := filepath.Join(workspace, "provider.sock")

	client, err := Start(ctx, StartOptions{Binary: binary, SocketPath: socketPath, TokenFile: tokenPath, StartupTimeout: 5 * time.Second})
	if err != nil {
		return report, &ConformanceError{Check: "process_start"}
	}
	descriptor, err := client.Describe(ctx)
	if err != nil {
		_ = client.Close()
		return report, &ConformanceError{Check: "descriptor"}
	}
	fingerprint, err := Fingerprint(descriptor)
	if err != nil {
		_ = client.Close()
		return report, &ConformanceError{Check: "descriptor_validation"}
	}
	report.ProviderID, report.ProviderVersion, report.Fingerprint = descriptor.ID, descriptor.Version, fingerprint
	report.add("descriptor", true)
	if err = client.Close(); err != nil {
		return report, &ConformanceError{Check: "shutdown"}
	}
	report.add("bounded_shutdown", true)
	client, err = Start(ctx, StartOptions{Binary: binary, SocketPath: socketPath, TokenFile: tokenPath, StartupTimeout: 5 * time.Second})
	if err != nil {
		return report, &ConformanceError{Check: "restart"}
	}
	defer client.Close()
	restarted, err := client.Describe(ctx)
	if err != nil {
		return report, &ConformanceError{Check: "restart_descriptor"}
	}
	restartedFingerprint, err := Fingerprint(restarted)
	if err != nil || restarted.ID != descriptor.ID || restarted.Name != descriptor.Name || restarted.Version != descriptor.Version || restarted.ProtocolVersion != descriptor.ProtocolVersion || restartedFingerprint != fingerprint {
		return report, &ConformanceError{Check: "descriptor_stability"}
	}
	report.add("descriptor_restart_stability", true)
	config := Config{}
	if supplied != nil {
		config = *supplied
	} else {
		config = defaultConformanceConfig(descriptor.ConfigurationSchema, filepath.Join(workspace, "objects"))
	}
	if err = client.Configure(ctx, config); err != nil {
		return report, &ConformanceError{Check: "configure"}
	}
	report.add("configure", true)
	if err = client.Probe(ctx); err != nil {
		return report, &ConformanceError{Check: "probe"}
	}
	report.add("probe", true)

	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return report, &ConformanceError{Check: "fixture_data"}
	}
	rootKey := "conformance/" + hex.EncodeToString(nonce[:])
	key := rootKey + "/object"
	payload := make([]byte, 384<<10)
	for i := range payload {
		payload[i] = byte((i*31 + 17) % 251)
	}
	initialHash := sha256.Sum256(payload)
	initialDigest := hex.EncodeToString(initialHash[:])
	info, err := client.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)))
	if err != nil || info.Size != int64(len(payload)) || info.SHA256 != initialDigest {
		return report, &ConformanceError{Check: "put"}
	}
	report.add("stream_put", true)
	stat, err := client.Stat(ctx, key)
	if err != nil || stat != info {
		return report, &ConformanceError{Check: "stat"}
	}
	report.add("stat", true)
	opened, gotInfo, err := client.Open(ctx, key)
	if err != nil {
		return report, &ConformanceError{Check: "read"}
	}
	readBack, readErr := io.ReadAll(io.LimitReader(opened, int64(len(payload))+1))
	closeErr := opened.Close()
	if readErr != nil || closeErr != nil || gotInfo != info || !bytes.Equal(readBack, payload) {
		return report, &ConformanceError{Check: "read"}
	}
	report.add("stream_read", true)
	start, length := int64(173), int64(5091)
	ranged, rangeInfo, err := client.OpenRange(ctx, key, start, length)
	if err != nil {
		return report, &ConformanceError{Check: "range_read"}
	}
	rangeBytes, readErr := io.ReadAll(io.LimitReader(ranged, length+1))
	closeErr = ranged.Close()
	if readErr != nil || closeErr != nil || rangeInfo != info || !bytes.Equal(rangeBytes, payload[start:start+length]) {
		return report, &ConformanceError{Check: "range_read"}
	}
	if reader, _, rangeErr := client.OpenRange(ctx, key, int64(len(payload)), 1); reader != nil || !isRemoteCode(rangeErr, "invalid_range") {
		if reader != nil {
			_ = reader.Close()
		}
		return report, &ConformanceError{Check: "invalid_range"}
	}
	report.add("range_read", true)

	replacement := []byte("complete replacement object")
	replacementHash := sha256.Sum256(replacement)
	replacementDigest := hex.EncodeToString(replacementHash[:])
	replaced, err := client.Put(ctx, key, bytes.NewReader(replacement), int64(len(replacement)))
	if err != nil || replaced.Size != int64(len(replacement)) || replaced.SHA256 != replacementDigest {
		return report, &ConformanceError{Check: "atomic_replace"}
	}
	readBack, err = readExactObject(ctx, client, key, replaced)
	if err != nil || !bytes.Equal(readBack, replacement) {
		return report, &ConformanceError{Check: "atomic_replace"}
	}
	report.add("atomic_replace", true)

	partial, partialErr := interruptedPut(ctx, client, key, len(replacement)+100)
	if partialErr == nil {
		return report, &ConformanceError{Check: "incomplete_put_rejected"}
	}
	readBack, err = readExactObject(ctx, client, key, replaced)
	if err != nil || !bytes.Equal(readBack, replacement) {
		return report, &ConformanceError{Check: "failed_replace_preserves_old"}
	}
	_ = partial
	report.add("failed_replace_preserves_old", true)

	for _, name := range []string{"a", "b", "c"} {
		bytesValue := []byte("page-" + name)
		if _, err = client.Put(ctx, rootKey+"/page-"+name, bytes.NewReader(bytesValue), int64(len(bytesValue))); err != nil {
			return report, &ConformanceError{Check: "list_setup"}
		}
	}
	first, err := client.List(ctx, rootKey, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" || first.Items[0].Key >= first.Items[1].Key {
		return report, &ConformanceError{Check: "list_first_page"}
	}
	second, err := client.List(ctx, rootKey, first.NextCursor, 2)
	if err != nil || len(second.Items) != 2 || second.Items[0].Key <= first.Items[1].Key || second.Items[1].Key <= second.Items[0].Key || second.NextCursor != "" {
		return report, &ConformanceError{Check: "list_second_page"}
	}
	report.add("list_pagination", true)

	if err = client.Delete(ctx, key); err != nil {
		return report, &ConformanceError{Check: "delete"}
	}
	if err = client.Delete(ctx, key); err != nil {
		return report, &ConformanceError{Check: "idempotent_delete"}
	}
	if _, err = client.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
		return report, &ConformanceError{Check: "missing_stat"}
	}
	if reader, _, openErr := client.Open(ctx, key); reader != nil {
		_ = reader.Close()
		return report, &ConformanceError{Check: "missing_read"}
	} else if !errors.Is(openErr, ErrNotFound) {
		return report, &ConformanceError{Check: "missing_read"}
	}
	report.add("delete_and_missing", true)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	started := time.Now()
	err = client.Probe(canceled)
	if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		return report, &ConformanceError{Check: "context_cancel"}
	}
	report.add("context_cancel", true)
	report.Passed = true
	return report, nil
}

func (r *ConformanceReport) add(name string, passed bool) {
	r.Checks = append(r.Checks, ConformanceCheck{Name: name, Pass: passed})
}

func isRemoteCode(err error, code string) bool {
	var remote *RemoteError
	return errors.As(err, &remote) && remote.Code == code
}

func defaultConformanceConfig(schema Schema, root string) Config {
	config := Config{Values: map[string]json.RawMessage{}, Secrets: map[string]string{}}
	for _, field := range schema.Fields {
		if field.Control == "secret" {
			if field.Required {
				config.Secrets[field.Key] = "conformance-placeholder"
			}
			continue
		}
		if len(field.Default) > 0 {
			config.Values[field.Key] = append(json.RawMessage(nil), field.Default...)
			continue
		}
		var value any
		switch field.Control {
		case "text":
			value = "conformance"
			if field.Key == "root" {
				value = root
			}
		case "boolean":
			value = true
		case "number":
			value = 1
		case "select":
			if len(field.Options) > 0 {
				value = field.Options[0].Value
			}
		}
		if value != nil {
			b, _ := json.Marshal(value)
			config.Values[field.Key] = b
		}
	}
	return config
}

func readExactObject(ctx context.Context, client *Client, key string, expected ObjectInfo) ([]byte, error) {
	r, info, err := client.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if info != expected || expected.Size > 1<<20 {
		return nil, ErrProtocol
	}
	b, err := io.ReadAll(io.LimitReader(r, expected.Size+1))
	if err != nil || int64(len(b)) != expected.Size {
		return nil, ErrProtocol
	}
	hash := sha256.Sum256(b)
	if hex.EncodeToString(hash[:]) != expected.SHA256 {
		return nil, ErrProtocol
	}
	return b, nil
}

func interruptedPut(ctx context.Context, client *Client, key string, declaredSize int) (ObjectInfo, error) {
	r, w := io.Pipe()
	done := make(chan struct {
		info ObjectInfo
		err  error
	}, 1)
	go func() {
		info, err := client.Put(ctx, key, r, int64(declaredSize))
		done <- struct {
			info ObjectInfo
			err  error
		}{info, err}
	}()
	_, _ = w.Write([]byte("partial"))
	_ = w.CloseWithError(errors.New("injected incomplete upload"))
	select {
	case result := <-done:
		return result.info, result.err
	case <-time.After(3 * time.Second):
		_ = client.Close()
		return ObjectInfo{}, fmt.Errorf("upload did not cancel")
	}
}
