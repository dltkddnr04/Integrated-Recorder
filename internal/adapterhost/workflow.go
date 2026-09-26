package adapterhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/pluginconfig"
)

const hostCloseTimeout = 20 * time.Second

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
	if err = adapterproto.ValidateResourceRefForDescriptor(d, resource); err != nil {
		return WorkflowProgress{}, fmt.Errorf("invalid resource reference")
	}
	input = marshalObject(adapterproto.ApplyDefaults(d.InputSchema, decodeObject(input)))
	wfID, err := newWorkflowID()
	if err != nil {
		return WorkflowProgress{}, err
	}
	if !hasCapability(d, adapterproto.CapabilityResolveWorkflow) {
		media, resolveErr := h.ResolveLegacy(ctx, id, input, resource)
		if resolveErr != nil {
			return WorkflowProgress{}, resolveErr
		}
		return WorkflowProgress{WorkflowID: wfID, AdapterID: id, State: "resolved", Resource: cloneResourceRef(resource), Media: &media, Provenance: provenance(d)}, nil
	}
	release, err := h.reserveWorkflow()
	if err != nil {
		return WorkflowProgress{}, err
	}
	defer release()
	effective, err := h.effective(id, resource)
	if err != nil {
		return WorkflowProgress{}, err
	}
	schema, err := h.Schema(id, resource)
	if err != nil {
		return WorkflowProgress{}, err
	}
	configuration, secrets, err := effectiveConfiguration(schema, effective, false)
	if err != nil {
		return WorkflowProgress{}, fmt.Errorf("effective adapter configuration is invalid: %w", err)
	}
	state, err := h.stateDocuments(id, resource)
	if err != nil {
		return WorkflowProgress{}, fmt.Errorf("adapter state is unavailable")
	}
	params := adapterproto.ResolveBeginParams{WorkflowID: wfID, Input: input, Resource: cloneResourceRef(resource), Configuration: configuration, Secrets: secrets, State: state}
	raw, generation, err := h.callGeneration(ctx, id, adapterproto.MethodResolveBegin, params, 0)
	if err != nil {
		return WorkflowProgress{}, err
	}
	var result adapterproto.ResolveWorkflowResult
	if json.Unmarshal(raw, &result) != nil {
		return WorkflowProgress{}, fmt.Errorf("adapter returned invalid workflow result")
	}
	if err = h.validateWorkflowResult(d, result, wfID, resource); err != nil {
		return WorkflowProgress{}, err
	}
	session := workflowSession{adapterID: id, workflowID: wfID, resource: cloneResourceRef(resource), generation: generation, createdAt: time.Now().UTC(), updatedAt: time.Now().UTC()}
	return h.advanceWorkflow(ctx, d, session, result)
}

// ResolveLegacy runs the stateless resolve operation. V1 adapters may return a
// MediaSource directly; the extensible result envelope may accompany it with
// adapter-owned state mutations.
func (h *Host) ResolveLegacy(ctx context.Context, id string, input json.RawMessage, resource *adapterproto.ResourceRef) (adapterproto.MediaSource, error) {
	d, err := h.Descriptor(id)
	if err != nil {
		return adapterproto.MediaSource{}, err
	}
	if err = adapterproto.ValidateObjectAgainstSchema(d.InputSchema, input); err != nil {
		return adapterproto.MediaSource{}, err
	}
	if err = adapterproto.ValidateResourceRefForDescriptor(d, resource); err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("invalid resource reference")
	}
	input = marshalObject(adapterproto.ApplyDefaults(d.InputSchema, decodeObject(input)))
	effective, err := h.effective(id, resource)
	if err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("adapter configuration unavailable")
	}
	schema, err := h.Schema(id, resource)
	if err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("adapter configuration schema unavailable")
	}
	configuration, secrets, err := effectiveConfiguration(schema, effective, true)
	if err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("effective adapter configuration is invalid: %w", err)
	}
	state, err := h.stateDocuments(id, resource)
	if err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("adapter state is unavailable")
	}
	params := adapterproto.ResolveParams{Input: input, Resource: cloneResourceRef(resource), Configuration: configuration, Secrets: secrets, State: state}
	raw, err := h.call(ctx, id, adapterproto.MethodResolve, params)
	if err != nil {
		return adapterproto.MediaSource{}, err
	}
	media, mutations, err := parseResolveResult(raw)
	if err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("adapter returned invalid media source")
	}
	if err = validateMediaForDescriptor(d, media); err != nil {
		return adapterproto.MediaSource{}, err
	}
	if err = h.applyStateMutations(id, resource, mutations); err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("adapter state could not be saved")
	}
	return media, nil
}

func parseResolveResult(raw json.RawMessage) (adapterproto.MediaSource, []adapterproto.StateMutation, error) {
	var shape map[string]json.RawMessage
	if json.Unmarshal(raw, &shape) != nil || shape == nil {
		return adapterproto.MediaSource{}, nil, fmt.Errorf("invalid resolve result")
	}
	if _, ok := shape["media"]; ok {
		var result adapterproto.ResolveResult
		if err := json.Unmarshal(raw, &result); err != nil {
			return adapterproto.MediaSource{}, nil, err
		}
		return result.Media, result.State, nil
	}
	var media adapterproto.MediaSource
	if err := json.Unmarshal(raw, &media); err != nil {
		return adapterproto.MediaSource{}, nil, err
	}
	return media, nil, nil
}

func effectiveConfiguration(schema adapterproto.Schema, effective pluginconfig.EffectiveDocument, require bool) (map[string]json.RawMessage, map[string]string, error) {
	combined := make(map[string]json.RawMessage, len(effective.EffectiveValues)+len(effective.EffectiveSecrets))
	for _, field := range schema.Fields {
		switch field.Control {
		case "secret":
			if value, ok := effective.EffectiveSecrets[field.Key]; ok {
				encoded, err := json.Marshal(value)
				if err != nil {
					return nil, nil, err
				}
				combined[field.Key] = encoded
			}
		case "action", "status":
			// Display-only schema fields never enter the adapter configuration.
		default:
			if value, ok := effective.EffectiveValues[field.Key]; ok {
				combined[field.Key] = append(json.RawMessage(nil), value...)
			}
		}
	}
	combined = adapterproto.ApplyDefaults(schema, combined)
	// Stored overrides can become hidden when their controlling value changes.
	// Keep them in storage so the user can reveal them again later, but do not
	// send or validate dormant values for this resolution.
	for _, field := range schema.Fields {
		if field.Control == "action" || field.Control == "status" {
			continue
		}
		visible, err := adapterproto.IsVisible(schema, field.Key, combined)
		if err != nil {
			return nil, nil, err
		}
		if !visible {
			delete(combined, field.Key)
		}
	}
	validate := adapterproto.ValidateProvidedValues
	if require {
		validate = adapterproto.ValidateValues
	}
	if err := validate(schema, combined); err != nil {
		return nil, nil, err
	}
	values := map[string]json.RawMessage{}
	for _, field := range schema.Fields {
		if field.Control == "secret" || field.Control == "action" || field.Control == "status" {
			continue
		}
		if raw, ok := combined[field.Key]; ok {
			values[field.Key] = append(json.RawMessage(nil), raw...)
		}
	}
	secrets := map[string]string{}
	for _, field := range schema.Fields {
		if field.Control == "secret" {
			if value, ok := effective.EffectiveSecrets[field.Key]; ok {
				if _, active := combined[field.Key]; active {
					secrets[field.Key] = value
				}
			}
		}
	}
	return values, secrets, nil
}

func decodeObject(raw json.RawMessage) map[string]json.RawMessage {
	values := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &values)
	return values
}

func marshalObject(values map[string]json.RawMessage) json.RawMessage {
	raw, _ := json.Marshal(values)
	return raw
}

func validateMediaForDescriptor(d adapterproto.Descriptor, media adapterproto.MediaSource) error {
	if err := adapterproto.ValidateMediaSource(media, d.MediaTypes); err != nil {
		return err
	}
	if media.RefreshPolicy != nil && !hasCapability(d, adapterproto.CapabilityRefresh) {
		return fmt.Errorf("adapter returned refresh policy without refresh capability")
	}
	return nil
}

func (h *Host) validateWorkflowResult(d adapterproto.Descriptor, result adapterproto.ResolveWorkflowResult, expectedID string, fallback *adapterproto.ResourceRef) error {
	if err := adapterproto.ValidateWorkflowResult(result, expectedID, d.MediaTypes); err != nil {
		return err
	}
	if result.Resource == nil {
		result.Resource = fallback
	}
	if err := adapterproto.ValidateResourceRefForDescriptor(d, result.Resource); err != nil {
		return fmt.Errorf("adapter returned an invalid resource")
	}
	if result.Media != nil {
		if err := validateMediaForDescriptor(d, *result.Media); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) advanceWorkflow(ctx context.Context, d adapterproto.Descriptor, session workflowSession, result adapterproto.ResolveWorkflowResult) (WorkflowProgress, error) {
	for {
		if session.continuing && !h.continuationActive(session) {
			return WorkflowProgress{}, fmt.Errorf("workflow canceled")
		}
		session.transitions++
		if session.transitions > maxWorkflowTransitions {
			return WorkflowProgress{}, fmt.Errorf("adapter workflow exceeded transition limit")
		}
		if result.Resource == nil {
			result.Resource = cloneResourceRef(session.resource)
		}
		if err := h.validateWorkflowResult(d, result, session.workflowID, session.resource); err != nil {
			return WorkflowProgress{}, err
		}
		if err := h.applyStateMutations(session.adapterID, result.Resource, result.StateMutations); err != nil {
			return WorkflowProgress{}, fmt.Errorf("adapter state could not be saved")
		}
		if result.State != "resource_discovered" {
			break
		}
		session.resource = cloneResourceRef(result.Resource)
		effective, err := h.effective(session.adapterID, session.resource)
		if err != nil {
			return WorkflowProgress{}, err
		}
		schemaChain, err := h.schemaChain(session.adapterID, session.resource)
		if err != nil {
			return WorkflowProgress{}, err
		}
		configuration, secrets, err := effectiveConfiguration(mergeSchemas(schemaChain), effective, false)
		if err != nil {
			return WorkflowProgress{}, fmt.Errorf("effective adapter configuration is invalid")
		}
		state, err := h.stateDocuments(session.adapterID, session.resource)
		if err != nil {
			return WorkflowProgress{}, fmt.Errorf("adapter state is unavailable")
		}
		if session.continuing && !h.continuationActive(session) {
			return WorkflowProgress{}, fmt.Errorf("workflow canceled")
		}
		params := adapterproto.ResolveContinueParams{WorkflowID: result.WorkflowID, Resource: cloneResourceRef(session.resource), Configuration: configuration, Secrets: secrets, State: state}
		raw, err := h.callAtGeneration(ctx, session.adapterID, adapterproto.MethodResolveContinue, params, session.generation)
		if errors.Is(err, errAdapterGenerationChanged) {
			h.expireWorkflow(result.WorkflowID)
			return WorkflowProgress{}, errAdapterGenerationChanged
		}
		if err != nil {
			return WorkflowProgress{}, err
		}
		if json.Unmarshal(raw, &result) != nil {
			return WorkflowProgress{}, fmt.Errorf("adapter returned invalid workflow result")
		}
	}
	if session.continuing && !h.continuationActive(session) {
		return WorkflowProgress{}, fmt.Errorf("workflow canceled")
	}
	progress := WorkflowProgress{WorkflowID: result.WorkflowID, AdapterID: d.ID, State: result.State, Resource: cloneResourceRef(result.Resource), Challenge: result.Challenge, Media: result.Media, Provenance: provenance(d)}
	if result.Challenge != nil && result.Challenge.Prompt != nil {
		if _, err := h.interactions.Apply(*result.Challenge.Prompt); err != nil {
			return WorkflowProgress{}, fmt.Errorf("adapter prompt is invalid")
		}
	}
	now := time.Now().UTC()
	if result.State == "configuration_required" || result.State == "interaction_required" {
		session.resource = cloneResourceRef(result.Resource)
		session.progress = progress
		session.updatedAt = now
		h.mu.Lock()
		if existing, exists := h.workflows[result.WorkflowID]; exists {
			if session.continuing && (!existing.continuing || existing.continuationID != session.continuationID) {
				h.mu.Unlock()
				if newID := interactionID(progress); newID != "" {
					h.interactions.Remove(newID)
				}
				return WorkflowProgress{}, fmt.Errorf("workflow canceled")
			}
			session.continuing = existing.continuing
			session.continuationID = existing.continuationID
			if oldID := interactionID(existing.progress); oldID != "" && oldID != interactionID(progress) {
				h.interactions.Remove(oldID)
			}
		} else if session.continuing {
			h.mu.Unlock()
			if newID := interactionID(progress); newID != "" {
				h.interactions.Remove(newID)
			}
			return WorkflowProgress{}, fmt.Errorf("workflow canceled")
		} else if len(h.workflows) >= maxActiveWorkflows {
			h.mu.Unlock()
			return WorkflowProgress{}, fmt.Errorf("too many active workflows")
		}
		if session.createdAt.IsZero() {
			session.createdAt = now
		}
		h.workflows[result.WorkflowID] = session
		h.mu.Unlock()
	} else {
		h.removeWorkflow(result.WorkflowID)
	}
	return progress, nil
}

func (h *Host) continuationActive(session workflowSession) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.workflows[session.workflowID]
	return ok && current.continuing && current.continuationID == session.continuationID
}

func interactionID(progress WorkflowProgress) string {
	if progress.Challenge != nil && progress.Challenge.Prompt != nil {
		return progress.Challenge.Prompt.InteractionID
	}
	return ""
}

func (h *Host) reserveWorkflow() (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupWorkflowsLocked(time.Now().UTC())
	if h.closed {
		return nil, fmt.Errorf("adapter host is closed")
	}
	if len(h.workflows)+h.workflowStarts >= maxActiveWorkflows {
		return nil, fmt.Errorf("too many active workflows")
	}
	h.workflowStarts++
	var once bool
	return func() {
		h.mu.Lock()
		if !once {
			once = true
			h.workflowStarts--
		}
		h.mu.Unlock()
	}, nil
}

func (h *Host) cleanupWorkflowsLocked(now time.Time) {
	for id, session := range h.workflows {
		if now.Sub(session.updatedAt) > workflowTTL {
			h.removeWorkflowLocked(id, session)
		}
	}
	h.interactions.Cleanup()
}

func (h *Host) removeWorkflowLocked(id string, session workflowSession) {
	delete(h.workflows, id)
	if session.cancel != nil {
		session.cancel()
	}
	if interaction := interactionID(session.progress); interaction != "" {
		h.interactions.Remove(interaction)
	}
}

func (h *Host) removeWorkflow(id string) {
	h.mu.Lock()
	if session, ok := h.workflows[id]; ok {
		h.removeWorkflowLocked(id, session)
	}
	h.mu.Unlock()
}

func (h *Host) expireWorkflow(id string) {
	h.removeWorkflow(id)
}

func (h *Host) Workflow(id string) (WorkflowProgress, error) {
	h.mu.Lock()
	h.cleanupWorkflowsLocked(time.Now().UTC())
	session, ok := h.workflows[id]
	if !ok {
		h.mu.Unlock()
		return WorkflowProgress{}, fmt.Errorf("workflow not found or expired")
	}
	session.updatedAt = time.Now().UTC()
	h.workflows[id] = session
	h.mu.Unlock()
	return cloneProgress(session.progress), nil
}

func cloneProgress(progress WorkflowProgress) WorkflowProgress {
	data, _ := json.Marshal(progress)
	var copy WorkflowProgress
	_ = json.Unmarshal(data, &copy)
	return copy
}

func (h *Host) CancelWorkflow(id string) error {
	h.mu.Lock()
	h.cleanupWorkflowsLocked(time.Now().UTC())
	session, ok := h.workflows[id]
	if ok {
		h.removeWorkflowLocked(id, session)
	}
	h.mu.Unlock()
	if !ok {
		return fmt.Errorf("workflow not found or expired")
	}
	return nil
}

// ContinueResolution retains the v1 boolean persistence call shape. True opts
// into persistence for all optional fields that legacy challenges declared as
// persistable; explicit forbidden policies still take precedence.
func (h *Host) ContinueResolution(ctx context.Context, id string, values map[string]json.RawMessage, secrets map[string]string, persist bool) (WorkflowProgress, error) {
	fields := []string{}
	if persist {
		progress, err := h.Workflow(id)
		if err != nil || progress.Challenge == nil {
			return WorkflowProgress{}, fmt.Errorf("workflow challenge does not allow persistence")
		}
		if !progress.Challenge.Persistable {
			return WorkflowProgress{}, fmt.Errorf("workflow challenge does not allow persistence")
		}
		fields = legacyPersistableFields(progress.Challenge)
	}
	return h.ContinueResolutionFields(ctx, id, values, secrets, fields)
}

func legacyPersistableFields(challenge *adapterproto.WorkflowChallenge) []string {
	if challenge == nil {
		return nil
	}
	fields := make([]string, 0, len(challenge.Schema.Fields))
	for _, field := range challenge.Schema.Fields {
		if field.Control == "action" || field.Control == "status" {
			continue
		}
		if persistenceFor(field, challenge).Mode == adapterproto.PersistenceForbidden {
			continue
		}
		fields = append(fields, field.Key)
	}
	return fields
}

func (h *Host) ContinueResolutionFields(ctx context.Context, id string, values map[string]json.RawMessage, secrets map[string]string, persistFields []string) (WorkflowProgress, error) {
	session, err := h.lockWorkflowForContinue(id)
	if err != nil {
		return WorkflowProgress{}, err
	}
	callCtx, cancel := context.WithCancel(ctx)
	session.cancel = cancel
	defer cancel()
	defer h.unlockWorkflowContinue(id, session.continuationID)
	if !h.setWorkflowCancel(id, session.continuationID, cancel) {
		return WorkflowProgress{}, fmt.Errorf("workflow canceled")
	}
	if err = h.ensureWorkflowGeneration(callCtx, id, session); err != nil {
		return WorkflowProgress{}, err
	}
	challenge := session.progress.Challenge
	if challenge == nil {
		return WorkflowProgress{}, fmt.Errorf("workflow has no active challenge")
	}
	provided := map[string]json.RawMessage{}
	for key, value := range values {
		provided[key] = append(json.RawMessage(nil), value...)
	}
	for key, value := range secrets {
		encoded, _ := json.Marshal(value)
		if _, dup := provided[key]; dup {
			return WorkflowProgress{}, fmt.Errorf("challenge answer fields overlap")
		}
		provided[key] = encoded
	}
	if err := adapterproto.ValidateValues(challenge.Schema, provided); err != nil {
		return WorkflowProgress{}, err
	}
	ordinary, secretAnswers, err := splitAnswers(challenge.Schema, values, secrets)
	if err != nil {
		return WorkflowProgress{}, err
	}
	persistValues := ordinary
	persistSecrets := secretAnswers
	// Defaults fill the adapter-facing continuation payload, while persistence
	// still receives only values explicitly supplied by the user.
	ordinary = adapterproto.ApplyDefaults(challenge.Schema, ordinary)
	for _, field := range challenge.Schema.Fields {
		if field.Control == "action" || field.Control == "status" {
			delete(ordinary, field.Key)
		}
	}
	if _, err = h.persistChallengeAnswers(session, challenge, persistValues, persistSecrets, persistFields); err != nil {
		return WorkflowProgress{}, err
	}
	effective, err := h.effective(session.adapterID, session.resource)
	if err != nil {
		return WorkflowProgress{}, err
	}
	d, err := h.Descriptor(session.adapterID)
	if err != nil {
		return WorkflowProgress{}, err
	}
	schemaChain, err := h.schemaChain(session.adapterID, session.resource)
	if err != nil {
		return WorkflowProgress{}, err
	}
	configuration, configSecrets, err := effectiveConfiguration(mergeSchemas(schemaChain), effective, false)
	if err != nil {
		return WorkflowProgress{}, fmt.Errorf("effective adapter configuration is invalid")
	}
	state, err := h.stateDocuments(session.adapterID, session.resource)
	if err != nil {
		return WorkflowProgress{}, fmt.Errorf("adapter state is unavailable")
	}
	params := adapterproto.ResolveContinueParams{WorkflowID: id, Resource: cloneResourceRef(session.resource), Configuration: configuration, Secrets: configSecrets, Answers: ordinary, AnswerSecrets: secretAnswers, State: state}
	raw, err := h.callAtGeneration(callCtx, session.adapterID, adapterproto.MethodResolveContinue, params, session.generation)
	if errors.Is(err, errAdapterGenerationChanged) {
		h.expireWorkflow(id)
		return WorkflowProgress{}, errAdapterGenerationChanged
	}
	if err != nil {
		return WorkflowProgress{}, err
	}
	var result adapterproto.ResolveWorkflowResult
	if json.Unmarshal(raw, &result) != nil {
		return WorkflowProgress{}, fmt.Errorf("adapter returned invalid workflow result")
	}
	if err = h.validateWorkflowResult(d, result, id, session.resource); err != nil {
		return WorkflowProgress{}, err
	}
	return h.advanceWorkflow(callCtx, d, session, result)
}

func (h *Host) setWorkflowCancel(id string, token uint64, cancel context.CancelFunc) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.workflows[id]
	if !ok || !current.continuing || current.continuationID != token {
		return false
	}
	current.cancel = cancel
	h.workflows[id] = current
	return true
}

func (h *Host) lockWorkflowForContinue(id string) (workflowSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupWorkflowsLocked(time.Now().UTC())
	session, ok := h.workflows[id]
	if !ok {
		return workflowSession{}, fmt.Errorf("workflow not found or expired")
	}
	if session.continuing {
		return workflowSession{}, fmt.Errorf("workflow is already continuing")
	}
	h.workflowCall++
	session.continuing = true
	session.continuationID = h.workflowCall
	session.updatedAt = time.Now().UTC()
	h.workflows[id] = session
	return session, nil
}

func (h *Host) unlockWorkflowContinue(id string, token uint64) {
	h.mu.Lock()
	if current, ok := h.workflows[id]; ok && current.continuing && current.continuationID == token {
		current.continuing = false
		current.continuationID = 0
		current.cancel = nil
		current.updatedAt = time.Now().UTC()
		h.workflows[id] = current
	}
	h.mu.Unlock()
}

func (h *Host) ensureWorkflowGeneration(ctx context.Context, id string, session workflowSession) error {
	e, err := h.entryFor(session.adapterID)
	if err != nil {
		return err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	if _, err = h.ensureProcess(ctx, e); err != nil {
		return err
	}
	e.stateMu.Lock()
	generation := e.status.Generation
	e.stateMu.Unlock()
	if generation != session.generation {
		h.expireWorkflow(id)
		return errAdapterGenerationChanged
	}
	return nil
}

func splitAnswers(schema adapterproto.Schema, values map[string]json.RawMessage, secrets map[string]string) (map[string]json.RawMessage, map[string]string, error) {
	ordinary, secretAnswers := map[string]json.RawMessage{}, map[string]string{}
	knownValues, knownSecrets := map[string]bool{}, map[string]bool{}
	for _, field := range schema.Fields {
		if field.Control == "action" || field.Control == "status" {
			continue
		}
		if field.Control == "secret" {
			knownSecrets[field.Key] = true
			if value, ok := secrets[field.Key]; ok {
				secretAnswers[field.Key] = value
			}
			continue
		}
		knownValues[field.Key] = true
		if value, ok := values[field.Key]; ok {
			ordinary[field.Key] = append(json.RawMessage(nil), value...)
		}
	}
	for key := range values {
		if !knownValues[key] {
			return nil, nil, fmt.Errorf("challenge answers contain unknown or mismatched fields")
		}
	}
	for key := range secrets {
		if !knownSecrets[key] {
			return nil, nil, fmt.Errorf("challenge answers contain unknown or mismatched fields")
		}
	}
	return ordinary, secretAnswers, nil
}

func persistenceFor(field adapterproto.Field, challenge *adapterproto.WorkflowChallenge) adapterproto.FieldPersistence {
	if field.Persistence != nil {
		return *field.Persistence
	}
	if challenge.Persistable {
		return adapterproto.FieldPersistence{Mode: adapterproto.PersistenceOptional, Target: adapterproto.PersistenceTarget{Scope: adapterproto.PersistenceCurrent}}
	}
	return adapterproto.FieldPersistence{Mode: adapterproto.PersistenceForbidden, Target: adapterproto.PersistenceTarget{Scope: adapterproto.PersistenceCurrent}}
}

func (h *Host) persistChallengeAnswers(session workflowSession, challenge *adapterproto.WorkflowChallenge, values map[string]json.RawMessage, secrets map[string]string, persistFields []string) (bool, error) {
	requested := map[string]bool{}
	for _, key := range persistFields {
		if requested[key] {
			return false, fmt.Errorf("duplicate persistence field")
		}
		requested[key] = true
	}
	fields := map[string]adapterproto.Field{}
	for _, field := range challenge.Schema.Fields {
		fields[field.Key] = field
	}
	for key := range requested {
		if _, ok := fields[key]; !ok {
			return false, fmt.Errorf("unknown persistence field")
		}
	}
	groups := map[string]*persistGroup{}
	chain, err := h.schemaChain(session.adapterID, session.resource)
	if err != nil {
		return false, err
	}
	for _, field := range challenge.Schema.Fields {
		policy := persistenceFor(field, challenge)
		persist := policy.Mode == adapterproto.PersistenceRequired || policy.Mode == adapterproto.PersistenceOptional && requested[field.Key]
		if requested[field.Key] && policy.Mode == adapterproto.PersistenceForbidden {
			return false, fmt.Errorf("challenge field cannot be persisted")
		}
		if !persist {
			continue
		}
		if h.configs == nil {
			return false, fmt.Errorf("persistent configuration is unavailable")
		}
		scope, err := persistenceScope(session.adapterID, session.resource, policy.Target)
		if err != nil {
			return false, err
		}
		targetSchema, ok := schemaAtScope(chain, scope)
		if !ok {
			return false, fmt.Errorf("persistence target is outside the resource chain")
		}
		declared, ok := findSchemaField(targetSchema, field.Key)
		if !ok || declared.Control != field.Control {
			return false, fmt.Errorf("persistent challenge field is not declared by the target schema")
		}
		key := pluginconfig.ScopeIdentity(scope)
		group := groups[key]
		if group == nil {
			group = &persistGroup{scope: scope, schema: targetSchema, values: map[string]json.RawMessage{}, secrets: map[string]string{}}
			groups[string(key)] = group
		}
		if field.Control == "secret" {
			if value, exists := secrets[field.Key]; exists {
				group.secrets[field.Key] = value
			}
		} else if value, exists := values[field.Key]; exists {
			group.values[field.Key] = append(json.RawMessage(nil), value...)
		}
	}
	for key := range requested {
		if persistenceFor(fields[key], challenge).Mode == adapterproto.PersistenceForbidden {
			return false, fmt.Errorf("challenge field cannot be persisted")
		}
	}
	updates := make([]pluginconfig.ScopeUpdate, 0, len(groups))
	for _, scope := range chain {
		if group := groups[pluginconfig.ScopeIdentity(scope.Scope)]; group != nil {
			updates = append(updates, pluginconfig.ScopeUpdate{Scope: group.scope, Schema: group.schema, Values: group.values, Secrets: group.secrets})
		}
	}
	if err := h.configs.PutBatch(updates); err != nil {
		return false, fmt.Errorf("persistent configuration could not be saved")
	}
	return len(groups) != 0, nil
}

type persistGroup struct {
	scope   pluginconfig.Scope
	schema  adapterproto.Schema
	values  map[string]json.RawMessage
	secrets map[string]string
}

func persistenceScope(pluginID string, current *adapterproto.ResourceRef, target adapterproto.PersistenceTarget) (pluginconfig.Scope, error) {
	switch target.Scope {
	case adapterproto.PersistencePlugin:
		return pluginconfig.Scope{PluginID: pluginID}, nil
	case adapterproto.PersistenceCurrent:
		if current == nil {
			return pluginconfig.Scope{}, fmt.Errorf("current resource persistence target is unavailable")
		}
		return pluginconfig.Scope{PluginID: pluginID, Resource: cloneResourceRef(current)}, nil
	case adapterproto.PersistenceResource:
		if target.Resource == nil || !resourceInChain(current, target.Resource) {
			return pluginconfig.Scope{}, fmt.Errorf("persistence target is outside the resource chain")
		}
		return pluginconfig.Scope{PluginID: pluginID, Resource: cloneResourceRef(target.Resource)}, nil
	default:
		return pluginconfig.Scope{}, fmt.Errorf("invalid persistence target")
	}
}

func resourceInChain(current, target *adapterproto.ResourceRef) bool {
	if target == nil {
		return false
	}
	for item := current; item != nil; item = item.Parent {
		if sameResource(item, target) {
			return true
		}
	}
	return false
}

func sameResource(a, b *adapterproto.ResourceRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Type != b.Type || a.ID != b.ID {
		return false
	}
	return sameResource(a.Parent, b.Parent)
}

func schemaAtScope(chain []pluginconfig.ScopeSchema, scope pluginconfig.Scope) (adapterproto.Schema, bool) {
	identity := pluginconfig.ScopeIdentity(scope)
	for _, item := range chain {
		if pluginconfig.ScopeIdentity(item.Scope) == identity {
			return item.Schema, true
		}
	}
	return adapterproto.Schema{}, false
}

func findSchemaField(schema adapterproto.Schema, key string) (adapterproto.Field, bool) {
	for _, field := range schema.Fields {
		if field.Key == key {
			return field, true
		}
	}
	return adapterproto.Field{}, false
}

func cloneResourceRef(ref *adapterproto.ResourceRef) *adapterproto.ResourceRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	copy.Parent = cloneResourceRef(ref.Parent)
	return &copy
}

func (h *Host) stateDocuments(id string, resource *adapterproto.ResourceRef) ([]adapterproto.StateDocument, error) {
	scopes, err := pluginconfig.ResourceScopes(id, resource)
	if err != nil {
		return nil, err
	}
	documents := make([]pluginconfig.StateDoc, len(scopes))
	if h.state != nil {
		documents, err = h.state.Chain(scopes)
		if err != nil {
			return nil, err
		}
	}
	docs := make([]adapterproto.StateDocument, 0, len(scopes))
	for i, scope := range scopes {
		doc := documents[i]
		if doc.Values == nil {
			doc.Values = map[string]json.RawMessage{}
		}
		if doc.Secrets == nil {
			doc.Secrets = map[string]string{}
		}
		docs = append(docs, adapterproto.StateDocument{Resource: cloneResourceRef(scope.Resource), Values: doc.Values, Secrets: doc.Secrets})
	}
	return docs, nil
}

func (h *Host) applyStateMutations(id string, current *adapterproto.ResourceRef, mutations []adapterproto.StateMutation) error {
	commit, err := h.prepareStateMutations(id, current, mutations)
	if err != nil {
		return err
	}
	return commit()
}

func (h *Host) prepareStateMutations(id string, current *adapterproto.ResourceRef, mutations []adapterproto.StateMutation) (func() error, error) {
	if len(mutations) == 0 {
		return func() error { return nil }, nil
	}
	if h.state == nil {
		return nil, fmt.Errorf("adapter state store is unavailable")
	}
	d, err := h.Descriptor(id)
	if err != nil {
		return nil, err
	}
	if err = adapterproto.ValidateResourceRefForDescriptor(d, current); err != nil {
		return nil, fmt.Errorf("invalid resource chain")
	}
	allowed, err := pluginconfig.ResourceScopes(id, current)
	if err != nil {
		return nil, err
	}
	allowedScopes := map[string]pluginconfig.Scope{}
	for _, scope := range allowed {
		allowedScopes[pluginconfig.ScopeIdentity(scope)] = scope
	}
	items := make([]pluginconfig.StateMutation, 0, len(mutations))
	for _, mutation := range mutations {
		if mutation.Resource != nil {
			if err := adapterproto.ValidateResourceRefForDescriptor(d, mutation.Resource); err != nil {
				return nil, fmt.Errorf("adapter state target is invalid")
			}
		}
		scope := pluginconfig.Scope{PluginID: id, Resource: cloneResourceRef(mutation.Resource)}
		validated, ok := allowedScopes[pluginconfig.ScopeIdentity(scope)]
		if !ok {
			return nil, fmt.Errorf("adapter state target is outside the resource chain")
		}
		items = append(items, pluginconfig.StateMutation{Scope: validated, Values: mutation.Values, Secrets: mutation.Secrets, ClearValues: mutation.ClearValues, ClearSecrets: mutation.ClearSecrets})
	}
	return func() error { return h.state.Apply(items) }, nil
}

// ShouldRefreshProactively applies only the adapter-declared expiry window.
func ShouldRefreshProactively(media adapterproto.MediaSource, now time.Time) bool {
	policy := media.RefreshPolicy
	return policy != nil && policy.ExpiresAt != nil && !now.Before(policy.ExpiresAt.Add(-time.Duration(policy.RefreshBeforeSeconds)*time.Second))
}

// ShouldRefreshForStatus checks the adapter-declared status list. Core does not
// attach semantics to common HTTP status codes.
func ShouldRefreshForStatus(media adapterproto.MediaSource, status int) bool {
	if media.RefreshPolicy == nil {
		return false
	}
	for _, candidate := range media.RefreshPolicy.OnHTTPStatus {
		if candidate == status {
			return true
		}
	}
	return false
}

func (h *Host) Refresh(ctx context.Context, id string, resource *adapterproto.ResourceRef, current adapterproto.MediaSource) (adapterproto.MediaSource, error) {
	media, commit, err := h.PrepareRefresh(ctx, id, resource, current)
	if err != nil {
		return adapterproto.MediaSource{}, err
	}
	if err := commit(); err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("adapter state could not be saved")
	}
	return media, nil
}

// PrepareRefresh calls and validates the adapter's replacement media source,
// but defers adapter state writes until the caller has completed its own
// network-safety checks. The closure is idempotent for a single caller.
func (h *Host) PrepareRefresh(ctx context.Context, id string, resource *adapterproto.ResourceRef, current adapterproto.MediaSource) (adapterproto.MediaSource, func() error, error) {
	d, err := h.Descriptor(id)
	if err != nil {
		return adapterproto.MediaSource{}, nil, err
	}
	if !hasCapability(d, adapterproto.CapabilityRefresh) {
		return adapterproto.MediaSource{}, nil, fmt.Errorf("adapter does not support refresh")
	}
	if err := adapterproto.ValidateResourceRefForDescriptor(d, resource); err != nil {
		return adapterproto.MediaSource{}, nil, fmt.Errorf("invalid resource reference")
	}
	if err := adapterproto.ValidateMediaSource(current, d.MediaTypes); err != nil {
		return adapterproto.MediaSource{}, nil, fmt.Errorf("current media source is invalid")
	}
	state, err := h.stateDocuments(id, resource)
	if err != nil {
		return adapterproto.MediaSource{}, nil, fmt.Errorf("adapter state is unavailable")
	}
	params := adapterproto.RefreshParams{Resource: cloneResourceRef(resource), Current: current, State: state}
	raw, err := h.call(ctx, id, adapterproto.MethodRefresh, params)
	if err != nil {
		return adapterproto.MediaSource{}, nil, err
	}
	var result adapterproto.RefreshResult
	if json.Unmarshal(raw, &result) != nil {
		return adapterproto.MediaSource{}, nil, fmt.Errorf("adapter returned invalid refresh result")
	}
	if err := validateMediaForDescriptor(d, result.Media); err != nil {
		return adapterproto.MediaSource{}, nil, fmt.Errorf("adapter returned invalid refresh media source")
	}
	commit, err := h.prepareStateMutations(id, resource, result.State)
	if err != nil {
		return adapterproto.MediaSource{}, nil, fmt.Errorf("adapter state mutation is invalid")
	}
	return result.Media, commit, nil
}

func (h *Host) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	for id, session := range h.workflows {
		h.removeWorkflowLocked(id, session)
	}
	entries := make([]*entry, 0, len(h.entries))
	for _, e := range h.entries {
		entries = append(entries, e)
	}
	h.mu.Unlock()
	var wg sync.WaitGroup
	var closingMu sync.Mutex
	var closing []*process
	for _, e := range entries {
		wg.Add(1)
		go func(e *entry) {
			defer wg.Done()
			e.opMu.Lock()
			defer e.opMu.Unlock()
			e.stateMu.Lock()
			p := e.process
			e.process = nil
			e.stateMu.Unlock()
			if p != nil {
				closingMu.Lock()
				closing = append(closing, p)
				closingMu.Unlock()
				p.shutdown()
			}
		}(e)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(hostCloseTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		// A shared deadline bounds shutdown independently of adapter count. Kill
		// any process still registered, then join every closer goroutine.
		closingMu.Lock()
		toKill := append([]*process(nil), closing...)
		closingMu.Unlock()
		for _, e := range entries {
			e.stateMu.Lock()
			p := e.process
			e.stateMu.Unlock()
			if p != nil {
				toKill = append(toKill, p)
			}
		}
		for _, p := range toKill {
			p.kill()
		}
		<-done
	}
}
