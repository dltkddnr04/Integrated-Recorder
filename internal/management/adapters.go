package management

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
)

const adapterPreferenceFile = "adapters.json"

var adapterPreferenceIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type adapterPreferences struct {
	Disabled []string `json:"disabled_adapters"`
}

func (s *Store) loadAdapterPreferences() error {
	var preferences adapterPreferences
	if err := s.loadGlobal(adapterPreferenceFile, &preferences); err != nil {
		return err
	}
	if len(preferences.Disabled) > maxDisabledAdapters {
		return fmt.Errorf("disabled adapter list exceeds limit")
	}
	if s.disabledAdapters == nil {
		s.disabledAdapters = make(map[string]struct{}, len(preferences.Disabled))
	}
	previous := ""
	for _, id := range preferences.Disabled {
		if !adapterPreferenceIDRE.MatchString(id) || id <= previous {
			return fmt.Errorf("disabled adapter list is invalid")
		}
		previous = id
		s.disabledAdapters[id] = struct{}{}
	}
	return nil
}

// DisabledAdapters returns a sorted snapshot suitable for applying after
// adapter discovery. Adapters are enabled by default unless explicitly listed.
func (s *Store) DisabledAdapters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.disabledAdapters))
	for id := range s.disabledAdapters {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// AdapterEnabled reports the durable preference. Unknown valid identifiers
// default to enabled.
func (s *Store) AdapterEnabled(id string) bool {
	if !adapterPreferenceIDRE.MatchString(id) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, disabled := s.disabledAdapters[id]
	return !disabled
}

// SetAdapterEnabled persists an adapter's enabled preference with one atomic
// JSON snapshot. Runtime changes should happen only after this method succeeds.
func (s *Store) SetAdapterEnabled(id string, enabled bool) error {
	if !adapterPreferenceIDRE.MatchString(id) {
		return fmt.Errorf("invalid adapter identifier")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disabledAdapters == nil {
		s.disabledAdapters = map[string]struct{}{}
	}
	_, currentlyDisabled := s.disabledAdapters[id]
	if currentlyDisabled == !enabled {
		return nil
	}
	updated := make(map[string]struct{}, len(s.disabledAdapters)+1)
	for existing := range s.disabledAdapters {
		updated[existing] = struct{}{}
	}
	if enabled {
		delete(updated, id)
	} else {
		updated[id] = struct{}{}
	}
	if len(updated) > maxDisabledAdapters {
		return fmt.Errorf("disabled adapter list exceeds limit")
	}
	ids := make([]string, 0, len(updated))
	for disabledID := range updated {
		ids = append(ids, disabledID)
	}
	sort.Strings(ids)
	if err := s.write(filepath.Join(s.root, adapterPreferenceFile), adapterPreferences{Disabled: ids}); err != nil {
		return fmt.Errorf("save adapter preferences: %w", err)
	}
	s.disabledAdapters = updated
	return nil
}
