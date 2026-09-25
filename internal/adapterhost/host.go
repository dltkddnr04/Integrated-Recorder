// Package adapterhost discovers and supervises standalone adapter binaries.
// Only explicit adapter directories are scanned; PATH is never searched.
package adapterhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/interaction"
	"github.com/dltkddnr04/integrated-recorder/internal/pluginconfig"
)

const binaryPrefix = "integrated-recorder-adapter-"
const maxWorkflowTransitions = 16

type Status struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	Version         string `json:"version,omitempty"`
	ProtocolVersion int    `json:"protocol_version,omitempty"`
	State           string `json:"state"`
	Error           string `json:"error,omitempty"`
	Generation      uint64 `json:"generation,omitempty"`
	RestartAttempts int    `json:"restart_attempts,omitempty"`
}

type Adapter struct {
	Descriptor *adapterproto.Descriptor `json:"descriptor,omitempty"`
	Status     Status                   `json:"status"`
}

type entry struct {
	opMu            sync.Mutex
	stateMu         sync.Mutex
	descriptor      *adapterproto.Descriptor
	process         *process
	status          Status
	path            string
	nextRestart     time.Time
	baseFingerprint string
	restartRejected bool
}

type workflowSession struct {
	adapterID      string
	resource       *adapterproto.ResourceRef
	progress       WorkflowProgress
	continuing     bool
	continuationID uint64
}

type WorkflowProgress struct {
	WorkflowID string                          `json:"workflow_id"`
	AdapterID  string                          `json:"adapter_id"`
	State      string                          `json:"state"`
	Resource   *adapterproto.ResourceRef       `json:"resource,omitempty"`
	Challenge  *adapterproto.WorkflowChallenge `json:"challenge,omitempty"`
	Media      *adapterproto.MediaSource       `json:"media,omitempty"`
	Provenance adapterproto.AdapterProvenance  `json:"adapter,omitempty"`
}

type Host struct {
	mu           sync.RWMutex
	entries      map[string]*entry
	configs      *pluginconfig.Service
	interactions *interaction.Tracker
	workflows    map[string]workflowSession
	workflowCall uint64
	closed       bool
}

func Discover(ctx context.Context, dir string, configs *pluginconfig.Service) (*Host, error) {
	h := &Host{entries: map[string]*entry{}, configs: configs, interactions: interaction.NewTracker(), workflows: map[string]workflowSession{}}
	if strings.TrimSpace(dir) == "" {
		return h, nil
	}
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return h, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read adapter directory: %w", err)
	}
	paths := []string{}
	for _, item := range files {
		if !strings.HasPrefix(item.Name(), binaryPrefix) {
			continue
		}
		info, infoErr := item.Info()
		if infoErr != nil {
			continue
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			continue
		}
		paths = append(paths, filepath.Join(dir, item.Name()))
	}
	sort.Strings(paths)
	for _, path := range paths {
		candidateID := strings.TrimPrefix(filepath.Base(path), binaryPrefix)
		p, startErr := startProcess(path)
		if startErr != nil {
			h.entries["candidate:"+candidateID] = &entry{status: Status{ID: candidateID, State: "failed", Error: "adapter process could not start"}, path: path}
			continue
		}
		d, callErr := describeProcess(ctx, p)
		if callErr != nil {
			p.kill()
			h.entries["candidate:"+candidateID] = &entry{process: p, status: Status{ID: candidateID, State: "failed", Error: "adapter describe handshake failed"}, path: path}
			continue
		}
		fingerprint := descriptorFingerprint(d)
		if existing, ok := h.entries[d.ID]; ok {
			if existing.process != nil {
				existing.process.kill()
			}
			p.kill()
			existing.status = Status{ID: d.ID, State: "rejected", Error: "duplicate adapter id"}
			existing.process = nil
			existing.descriptor = nil
			continue
		}
		copy := d
		h.entries[d.ID] = &entry{descriptor: &copy, process: p, path: path, baseFingerprint: fingerprint, status: Status{ID: d.ID, Name: d.Name, Version: d.Version, ProtocolVersion: d.ProtocolVersion, State: "ready", Generation: 1}}
	}
	return h, nil
}

func describeProcess(ctx context.Context, p *process) (adapterproto.Descriptor, error) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := p.call(callCtx, adapterproto.MethodDescribe, map[string]any{})
	if err != nil {
		return adapterproto.Descriptor{}, err
	}
	var d adapterproto.Descriptor
	if json.Unmarshal(result, &d) != nil {
		return adapterproto.Descriptor{}, fmt.Errorf("invalid adapter descriptor")
	}
	if err := d.Validate(); err != nil {
		return adapterproto.Descriptor{}, fmt.Errorf("invalid adapter descriptor: %w", err)
	}
	return d, nil
}

func descriptorFingerprint(d adapterproto.Descriptor) string {
	data, _ := json.Marshal(d)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func refresh(e *entry) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.process != nil && e.process.isUnusable() && e.status.State == "ready" {
		e.status.State = "unavailable"
		e.status.Error = "adapter process needs restart"
	}
}

func (h *Host) List() []Adapter {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Adapter, 0, len(h.entries))
	for _, e := range h.entries {
		refresh(e)
		e.stateMu.Lock()
		item := Adapter{Status: e.status}
		if e.descriptor != nil {
			d := *e.descriptor
			item.Descriptor = &d
		}
		e.stateMu.Unlock()
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Status.ID < out[j].Status.ID })
	return out
}

func (h *Host) Get(id string) (Adapter, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.entries[id]
	if !ok {
		for _, candidate := range h.entries {
			if candidate.status.ID == id && candidate.descriptor == nil {
				e, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		return Adapter{}, fmt.Errorf("adapter not found")
	}
	refresh(e)
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	out := Adapter{Status: e.status}
	if e.descriptor != nil {
		d := *e.descriptor
		out.Descriptor = &d
	}
	return out, nil
}

func (h *Host) Descriptor(id string) (adapterproto.Descriptor, error) {
	a, err := h.Get(id)
	if err != nil {
		return adapterproto.Descriptor{}, err
	}
	if a.Descriptor == nil {
		return adapterproto.Descriptor{}, fmt.Errorf("adapter is unavailable")
	}
	return *a.Descriptor, nil
}

func (h *Host) Schema(id string, resource *adapterproto.ResourceRef) (adapterproto.Schema, error) {
	chain, err := h.schemaChain(id, resource)
	if err != nil {
		return adapterproto.Schema{}, err
	}
	return mergeSchemas(chain), nil
}

func (h *Host) schemaChain(id string, resource *adapterproto.ResourceRef) ([]pluginconfig.ScopeSchema, error) {
	scopes, err := pluginconfig.ResourceScopes(id, resource)
	if err != nil {
		return nil, err
	}
	d, err := h.Descriptor(id)
	if err != nil {
		return nil, err
	}
	chain := make([]pluginconfig.ScopeSchema, 0, len(scopes))
	merged := []adapterproto.Field{}
	for _, scope := range scopes {
		schema := d.ConfigurationSchema
		if scope.Resource != nil {
			schema = adapterproto.Schema{}
			for _, declared := range d.ResourceTypes {
				if declared.Type == scope.Resource.Type {
					schema = declared.ConfigurationSchema
					break
				}
			}
		}
		merged = mergeFieldSlice(merged, schema.Fields)
		chain = append(chain, pluginconfig.ScopeSchema{Scope: scope, Schema: adapterproto.Schema{Fields: append([]adapterproto.Field(nil), merged...)}})
	}
	return chain, nil
}

func mergeFieldSlice(existing, incoming []adapterproto.Field) []adapterproto.Field {
	fields := map[string]adapterproto.Field{}
	order := []string{}
	for _, field := range existing {
		if _, ok := fields[field.Key]; !ok {
			order = append(order, field.Key)
		}
		fields[field.Key] = field
	}
	for _, field := range incoming {
		if _, ok := fields[field.Key]; !ok {
			order = append(order, field.Key)
		}
		fields[field.Key] = field
	}
	out := make([]adapterproto.Field, 0, len(fields))
	for _, key := range order {
		out = append(out, fields[key])
	}
	return out
}

func mergeSchemas(chain []pluginconfig.ScopeSchema) adapterproto.Schema {
	fields := map[string]adapterproto.Field{}
	order := []string{}
	for _, item := range chain {
		for _, field := range item.Schema.Fields {
			if _, ok := fields[field.Key]; !ok {
				order = append(order, field.Key)
			}
			fields[field.Key] = field
		}
	}
	out := adapterproto.Schema{Fields: make([]adapterproto.Field, 0, len(fields))}
	for _, key := range order {
		out.Fields = append(out.Fields, fields[key])
	}
	return out
}

func (h *Host) effective(id string, resource *adapterproto.ResourceRef) (pluginconfig.EffectiveDocument, error) {
	chain, err := h.schemaChain(id, resource)
	if err != nil {
		return pluginconfig.EffectiveDocument{}, err
	}
	if h.configs == nil {
		return pluginconfig.EffectiveDocument{Stored: pluginconfig.Document{Values: map[string]json.RawMessage{}, Secrets: map[string]string{}}, EffectiveValues: map[string]json.RawMessage{}, EffectiveSecrets: map[string]string{}, EffectiveSecretConfigured: map[string]bool{}, ValueSources: map[string]string{}, SecretSources: map[string]string{}}, nil
	}
	return h.configs.Effective(chain)
}

func (h *Host) ConfigSnapshot(id string, resource *adapterproto.ResourceRef) (pluginconfig.Snapshot, error) {
	effective, err := h.effective(id, resource)
	if err != nil {
		return pluginconfig.Snapshot{}, err
	}
	return effective.Snapshot(), nil
}

func (h *Host) Resolve(ctx context.Context, id string, input json.RawMessage, resource *adapterproto.ResourceRef) (adapterproto.MediaSource, error) {
	d, err := h.Descriptor(id)
	if err != nil {
		return adapterproto.MediaSource{}, err
	}
	if hasCapability(d, adapterproto.CapabilityResolveWorkflow) {
		progress, beginErr := h.BeginResolution(ctx, id, input, resource)
		if beginErr != nil {
			return adapterproto.MediaSource{}, beginErr
		}
		if progress.State != "resolved" || progress.Media == nil {
			return adapterproto.MediaSource{}, fmt.Errorf("adapter resolution requires additional interaction")
		}
		return *progress.Media, nil
	}
	return h.ResolveLegacy(ctx, id, input, resource)
}

func hasCapability(d adapterproto.Descriptor, capability string) bool {
	for _, item := range d.Capabilities {
		if item == capability {
			return true
		}
	}
	return false
}

func (h *Host) entryFor(id string) (*entry, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return nil, fmt.Errorf("adapter host is closed")
	}
	e, ok := h.entries[id]
	if !ok || e.descriptor == nil {
		return nil, fmt.Errorf("adapter is unavailable")
	}
	return e, nil
}

func (h *Host) ensureProcess(ctx context.Context, e *entry) (*process, error) {
	h.mu.RLock()
	closed := h.closed
	h.mu.RUnlock()
	if closed {
		return nil, fmt.Errorf("adapter host is closed")
	}
	e.stateMu.Lock()
	if e.restartRejected {
		e.stateMu.Unlock()
		return nil, fmt.Errorf("adapter restart was rejected")
	}
	if e.process != nil && !e.process.isUnusable() {
		p := e.process
		e.stateMu.Unlock()
		return p, nil
	}
	if e.process != nil {
		e.process = nil
	}
	if !e.nextRestart.IsZero() && time.Now().Before(e.nextRestart) {
		e.stateMu.Unlock()
		return nil, fmt.Errorf("adapter restart is backing off")
	}
	e.status.RestartAttempts++
	attempt := e.status.RestartAttempts
	e.stateMu.Unlock()
	p, err := startProcess(e.path)
	if err == nil {
		var d adapterproto.Descriptor
		d, err = describeProcess(ctx, p)
		if err == nil && (d.ID != e.descriptor.ID || d.ProtocolVersion != e.descriptor.ProtocolVersion || d.Version != e.descriptor.Version) {
			err = fmt.Errorf("adapter descriptor identity changed")
		}
	}
	if err != nil {
		if p != nil {
			p.kill()
		}
		e.stateMu.Lock()
		if strings.Contains(err.Error(), "descriptor identity changed") || strings.Contains(err.Error(), "unsupported protocol version") {
			e.restartRejected = true
		}
		if !e.restartRejected {
			delay := 100 * time.Millisecond << min(attempt-1, 8)
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
			e.nextRestart = time.Now().Add(delay)
		}
		e.status.State = "unavailable"
		e.status.Error = "adapter restart failed validation"
		if e.restartRejected {
			e.status.State = "rejected"
		}
		e.stateMu.Unlock()
		return nil, fmt.Errorf("adapter process could not be restarted")
	}
	h.mu.RLock()
	if h.closed {
		h.mu.RUnlock()
		p.kill()
		return nil, fmt.Errorf("adapter host is closed")
	}
	e.stateMu.Lock()
	e.process = p
	e.nextRestart = time.Time{}
	e.status.State = "ready"
	e.status.Error = ""
	e.status.Generation++
	e.status.RestartAttempts = 0
	e.stateMu.Unlock()
	h.mu.RUnlock()
	return p, nil
}

func (h *Host) call(ctx context.Context, id, method string, params any) (json.RawMessage, error) {
	e, err := h.entryFor(id)
	if err != nil {
		return nil, err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	p, err := h.ensureProcess(ctx, e)
	if err != nil {
		return nil, err
	}
	result, err := p.call(ctx, method, params)
	if p.isUnusable() {
		e.stateMu.Lock()
		defer e.stateMu.Unlock()
		e.process = nil
		e.status.State = "unavailable"
		e.status.Error = "adapter process needs restart"
		if e.nextRestart.IsZero() {
			e.nextRestart = time.Now().Add(100 * time.Millisecond)
		}
	}
	return result, err
}

func newWorkflowID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func provenance(d adapterproto.Descriptor) adapterproto.AdapterProvenance {
	return adapterproto.AdapterProvenance{ID: d.ID, Version: d.Version, ProtocolVersion: d.ProtocolVersion, Fingerprint: descriptorFingerprint(d)}
}

func (h *Host) BeginResolution(ctx context.Context, id string, input json.RawMessage, resource *adapterproto.ResourceRef) (WorkflowProgress, error) {
	d, err := h.Descriptor(id)
	if err != nil {
		return WorkflowProgress{}, err
	}
	if err = adapterproto.ValidateObjectAgainstSchema(d.InputSchema, input); err != nil {
		return WorkflowProgress{}, err
	}
	if err = adapterproto.ValidateResourceRef(resource); err != nil {
		return WorkflowProgress{}, fmt.Errorf("invalid resource reference")
	}
	wfID, err := newWorkflowID()
	if err != nil {
		return WorkflowProgress{}, err
	}
	if !hasCapability(d, adapterproto.CapabilityResolveWorkflow) {
		media, resolveErr := h.ResolveLegacy(ctx, id, input, resource)
		if resolveErr != nil {
			return WorkflowProgress{}, resolveErr
		}
		return WorkflowProgress{WorkflowID: wfID, AdapterID: id, State: "resolved", Resource: resource, Media: &media, Provenance: provenance(d)}, nil
	}
	eff, err := h.effective(id, resource)
	if err != nil {
		return WorkflowProgress{}, err
	}
	params := adapterproto.ResolveBeginParams{WorkflowID: wfID, Input: input, Resource: resource, Configuration: eff.EffectiveValues, Secrets: eff.EffectiveSecrets}
	raw, err := h.call(ctx, id, adapterproto.MethodResolveBegin, params)
	if err != nil {
		return WorkflowProgress{}, err
	}
	var result adapterproto.ResolveWorkflowResult
	if json.Unmarshal(raw, &result) != nil {
		return WorkflowProgress{}, fmt.Errorf("adapter returned invalid workflow result")
	}
	if err = adapterproto.ValidateWorkflowResult(result, wfID, d.MediaTypes); err != nil {
		return WorkflowProgress{}, err
	}
	return h.advanceWorkflow(ctx, d, result, 0, resource)
}

// ResolveLegacy is the non-workflow adapter path. Input validation is shared
// with workflow adapters and configuration is still resolved by resource chain.
func (h *Host) ResolveLegacy(ctx context.Context, id string, input json.RawMessage, resource *adapterproto.ResourceRef) (adapterproto.MediaSource, error) {
	d, err := h.Descriptor(id)
	if err != nil {
		return adapterproto.MediaSource{}, err
	}
	if err = adapterproto.ValidateObjectAgainstSchema(d.InputSchema, input); err != nil {
		return adapterproto.MediaSource{}, err
	}
	if err = adapterproto.ValidateResourceRef(resource); err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("invalid resource reference")
	}
	eff, err := h.effective(id, resource)
	if err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("adapter configuration unavailable")
	}
	schema, err := h.Schema(id, resource)
	if err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("adapter configuration schema unavailable")
	}
	if err = validateEffectiveConfiguration(schema, eff); err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("effective adapter configuration is invalid: %w", err)
	}
	params := adapterproto.ResolveParams{Input: input, Resource: resource, Configuration: eff.EffectiveValues, Secrets: eff.EffectiveSecrets}
	raw, err := h.call(ctx, id, adapterproto.MethodResolve, params)
	if err != nil {
		return adapterproto.MediaSource{}, err
	}
	var media adapterproto.MediaSource
	if json.Unmarshal(raw, &media) != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("adapter returned invalid media source")
	}
	if err = adapterproto.ValidateMediaSource(media, d.MediaTypes); err != nil {
		return adapterproto.MediaSource{}, err
	}
	return media, nil
}

func validateEffectiveConfiguration(schema adapterproto.Schema, effective pluginconfig.EffectiveDocument) error {
	values := make(map[string]json.RawMessage, len(effective.EffectiveValues)+len(effective.EffectiveSecrets))
	for key, value := range effective.EffectiveValues {
		values[key] = append(json.RawMessage(nil), value...)
	}
	for key, value := range effective.EffectiveSecrets {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("invalid effective secret configuration")
		}
		values[key] = encoded
	}
	return adapterproto.ValidateValues(schema, values)
}

func (h *Host) advanceWorkflow(ctx context.Context, d adapterproto.Descriptor, result adapterproto.ResolveWorkflowResult, hops int, resource *adapterproto.ResourceRef) (WorkflowProgress, error) {
	if result.Resource == nil {
		result.Resource = resource
	}
	for transitions := 0; result.State == "resource_discovered"; transitions++ {
		if hops+transitions >= maxWorkflowTransitions {
			return WorkflowProgress{}, fmt.Errorf("adapter workflow exceeded transition limit")
		}
		resource = result.Resource
		eff, err := h.effective(d.ID, resource)
		if err != nil {
			return WorkflowProgress{}, err
		}
		params := adapterproto.ResolveContinueParams{WorkflowID: result.WorkflowID, Resource: resource, Configuration: eff.EffectiveValues, Secrets: eff.EffectiveSecrets}
		raw, err := h.call(ctx, d.ID, adapterproto.MethodResolveContinue, params)
		if err != nil {
			return WorkflowProgress{}, err
		}
		if json.Unmarshal(raw, &result) != nil {
			return WorkflowProgress{}, fmt.Errorf("adapter returned invalid workflow result")
		}
		if err = adapterproto.ValidateWorkflowResult(result, params.WorkflowID, d.MediaTypes); err != nil {
			return WorkflowProgress{}, err
		}
		if result.Resource == nil {
			result.Resource = resource
		}
	}
	progress := WorkflowProgress{WorkflowID: result.WorkflowID, AdapterID: d.ID, State: result.State, Resource: result.Resource, Challenge: result.Challenge, Media: result.Media, Provenance: provenance(d)}
	if result.Challenge != nil && result.Challenge.Prompt != nil {
		if _, err := h.interactions.Apply(*result.Challenge.Prompt); err != nil {
			return WorkflowProgress{}, fmt.Errorf("adapter prompt is invalid")
		}
	}
	if result.State == "configuration_required" || result.State == "interaction_required" {
		h.mu.Lock()
		continuing := false
		var continuationID uint64
		if current, exists := h.workflows[result.WorkflowID]; exists {
			continuing = current.continuing
			continuationID = current.continuationID
		}
		h.workflows[result.WorkflowID] = workflowSession{adapterID: d.ID, resource: result.Resource, progress: progress, continuing: continuing, continuationID: continuationID}
		h.mu.Unlock()
	} else {
		h.mu.Lock()
		delete(h.workflows, result.WorkflowID)
		h.mu.Unlock()
	}
	return progress, nil
}

func (h *Host) Workflow(id string) (WorkflowProgress, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	session, ok := h.workflows[id]
	if !ok {
		return WorkflowProgress{}, fmt.Errorf("workflow not found")
	}
	return session.progress, nil
}

func (h *Host) ContinueResolution(ctx context.Context, id string, values map[string]json.RawMessage, secrets map[string]string, persist bool) (WorkflowProgress, error) {
	h.mu.Lock()
	session, ok := h.workflows[id]
	if !ok {
		h.mu.Unlock()
		return WorkflowProgress{}, fmt.Errorf("workflow not found")
	}
	if session.continuing {
		h.mu.Unlock()
		return WorkflowProgress{}, fmt.Errorf("workflow is already continuing")
	}
	h.workflowCall++
	continuationID := h.workflowCall
	session.continuing = true
	session.continuationID = continuationID
	h.workflows[id] = session
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if current, exists := h.workflows[id]; exists && current.continuing && current.continuationID == continuationID {
			current.continuing = false
			current.continuationID = 0
			h.workflows[id] = current
		}
		h.mu.Unlock()
	}()
	challenge := session.progress.Challenge
	if challenge == nil {
		return WorkflowProgress{}, fmt.Errorf("workflow has no active challenge")
	}
	if persist && !challenge.Persistable {
		return WorkflowProgress{}, fmt.Errorf("workflow challenge does not allow persistence")
	}
	provided := map[string]json.RawMessage{}
	for key, value := range values {
		provided[key] = value
	}
	for key, value := range secrets {
		encoded, _ := json.Marshal(value)
		provided[key] = encoded
	}
	if err := adapterproto.ValidateValues(challenge.Schema, provided); err != nil {
		return WorkflowProgress{}, err
	}
	ordinary := map[string]json.RawMessage{}
	secretAnswers := map[string]string{}
	for _, field := range challenge.Schema.Fields {
		if field.Control == "action" || field.Control == "status" {
			continue
		}
		if field.Control == "secret" {
			if value, exists := secrets[field.Key]; exists {
				secretAnswers[field.Key] = value
			}
			continue
		}
		if value, exists := values[field.Key]; exists {
			ordinary[field.Key] = value
		}
	}
	if len(values) != len(ordinary) || len(secrets) != len(secretAnswers) {
		return WorkflowProgress{}, fmt.Errorf("challenge answers contain unknown or mismatched fields")
	}
	if persist && h.configs == nil {
		return WorkflowProgress{}, fmt.Errorf("persistent configuration is unavailable")
	}
	if persist && h.configs != nil {
		schema, err := h.Schema(session.adapterID, session.resource)
		if err != nil {
			return WorkflowProgress{}, err
		}
		for _, field := range challenge.Schema.Fields {
			if !containsField(schema, field.Key) {
				schema.Fields = append(schema.Fields, field)
			}
		}
		if err = h.configs.PutPartial(pluginconfig.Scope{PluginID: session.adapterID, Resource: session.resource}, schema, ordinary, secretAnswers, nil); err != nil {
			return WorkflowProgress{}, err
		}
	}
	eff, err := h.effective(session.adapterID, session.resource)
	if err != nil {
		return WorkflowProgress{}, err
	}
	params := adapterproto.ResolveContinueParams{WorkflowID: id, Resource: session.resource, Configuration: eff.EffectiveValues, Secrets: eff.EffectiveSecrets, Answers: ordinary, AnswerSecrets: secretAnswers}
	d, err := h.Descriptor(session.adapterID)
	if err != nil {
		return WorkflowProgress{}, err
	}
	raw, err := h.call(ctx, session.adapterID, adapterproto.MethodResolveContinue, params)
	if err != nil {
		return WorkflowProgress{}, err
	}
	var result adapterproto.ResolveWorkflowResult
	if json.Unmarshal(raw, &result) != nil {
		return WorkflowProgress{}, fmt.Errorf("adapter returned invalid workflow result")
	}
	if err = adapterproto.ValidateWorkflowResult(result, id, d.MediaTypes); err != nil {
		return WorkflowProgress{}, err
	}
	return h.advanceWorkflow(ctx, d, result, 1, session.resource)
}

func containsField(schema adapterproto.Schema, key string) bool {
	for _, field := range schema.Fields {
		if field.Key == key {
			return true
		}
	}
	return false
}

func (h *Host) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	list := []*entry{}
	for _, e := range h.entries {
		list = append(list, e)
	}
	h.mu.Unlock()
	for _, e := range list {
		e.opMu.Lock()
		e.stateMu.Lock()
		p := e.process
		e.process = nil
		e.stateMu.Unlock()
		if p != nil {
			p.shutdown()
		}
		e.opMu.Unlock()
	}
}
