// Package adapterhost discovers and supervises standalone adapter binaries.
// Only explicit adapter directories are scanned; PATH is never searched.
package adapterhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
	"github.com/dltkddnr04/integrated-recorder/internal/pluginconfig"
)

const binaryPrefix = "integrated-recorder-adapter-"

type Status struct {
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	Version         string `json:"version,omitempty"`
	ProtocolVersion int    `json:"protocol_version,omitempty"`
	State           string `json:"state"`
	Error           string `json:"error,omitempty"`
}

type Adapter struct {
	Descriptor *adapterproto.Descriptor `json:"descriptor,omitempty"`
	Status     Status                   `json:"status"`
}
type entry struct {
	descriptor *adapterproto.Descriptor
	process    *process
	status     Status
	path       string
}

type Host struct {
	mu      sync.RWMutex
	entries map[string]*entry
	configs *pluginconfig.Service
	closed  bool
}

func Discover(ctx context.Context, dir string, configs *pluginconfig.Service) (*Host, error) {
	h := &Host{entries: map[string]*entry{}, configs: configs}
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
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		result, callErr := p.call(callCtx, adapterproto.MethodDescribe, map[string]any{})
		cancel()
		if callErr != nil {
			p.kill()
			h.entries["candidate:"+candidateID] = &entry{process: p, status: Status{ID: candidateID, State: "failed", Error: "adapter describe handshake failed"}, path: path}
			continue
		}
		var descriptor adapterproto.Descriptor
		if json.Unmarshal(result, &descriptor) != nil || descriptor.Validate() != nil {
			p.kill()
			h.entries["candidate:"+candidateID] = &entry{process: p, status: Status{ID: candidateID, State: "failed", Error: "adapter descriptor is invalid"}, path: path}
			continue
		}
		d := descriptor
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
		status := Status{ID: d.ID, Name: d.Name, Version: d.Version, ProtocolVersion: d.ProtocolVersion, State: "ready"}
		h.entries[d.ID] = &entry{descriptor: &d, process: p, status: status, path: path}
	}
	return h, nil
}

func refresh(e *entry) {
	if e.process != nil && e.status.State == "ready" {
		select {
		case <-e.process.done:
			e.status.State = "unavailable"
			e.status.Error = "adapter process exited"
		default:
		}
	}
}
func (h *Host) List() []Adapter {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Adapter, 0, len(h.entries))
	for _, e := range h.entries {
		refresh(e)
		item := Adapter{Status: e.status}
		if e.descriptor != nil {
			d := *e.descriptor
			item.Descriptor = &d
		}
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
	if err := adapterproto.ValidateResourceRef(resource); err != nil {
		return adapterproto.Schema{}, fmt.Errorf("invalid resource reference")
	}
	d, err := h.Descriptor(id)
	if err != nil {
		return adapterproto.Schema{}, err
	}
	schema := d.ConfigurationSchema
	if resource != nil {
		for _, r := range d.ResourceTypes {
			if r.Type == resource.Type {
				schema = r.ConfigurationSchema
				break
			}
		}
	}
	return schema, nil
}

func (h *Host) Resolve(ctx context.Context, id string, input json.RawMessage, resource *adapterproto.ResourceRef) (adapterproto.MediaSource, error) {
	if err := adapterproto.ValidateObject(input); err != nil {
		return adapterproto.MediaSource{}, err
	}
	if err := adapterproto.ValidateResourceRef(resource); err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("invalid resource reference")
	}
	h.mu.RLock()
	e, ok := h.entries[id]
	if !ok || e.descriptor == nil || e.process == nil || e.status.State != "ready" {
		h.mu.RUnlock()
		return adapterproto.MediaSource{}, fmt.Errorf("adapter is unavailable")
	}
	d := *e.descriptor
	p := e.process
	h.mu.RUnlock()
	var values map[string]json.RawMessage
	var secrets map[string]string
	if h.configs != nil {
		doc, err := h.configs.Get(pluginconfig.Scope{PluginID: id, Resource: resource})
		if err != nil {
			return adapterproto.MediaSource{}, fmt.Errorf("adapter configuration unavailable")
		}
		values, secrets = doc.Values, doc.Secrets
	}
	params := adapterproto.ResolveParams{Input: input, Resource: resource, Configuration: values, Secrets: secrets}
	result, err := p.call(ctx, adapterproto.MethodResolve, params)
	if err != nil {
		select {
		case <-p.done:
			h.markUnavailable(id, p)
		default:
		}
		return adapterproto.MediaSource{}, err
	}
	var media adapterproto.MediaSource
	if err = json.Unmarshal(result, &media); err != nil {
		return adapterproto.MediaSource{}, fmt.Errorf("adapter returned invalid media source")
	}
	if err = adapterproto.ValidateMediaSource(media, d.MediaTypes); err != nil {
		return adapterproto.MediaSource{}, err
	}
	return media, nil
}

func (h *Host) markUnavailable(id string, p *process) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e, ok := h.entries[id]; ok && e.process == p {
		e.status.State = "unavailable"
		e.status.Error = "adapter process exited"
	}
}

func (h *Host) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	list := make([]*process, 0, len(h.entries))
	for _, e := range h.entries {
		if e.process != nil {
			list = append(list, e.process)
			e.process = nil
		}
	}
	h.mu.Unlock()
	for _, p := range list {
		p.shutdown()
	}
}
