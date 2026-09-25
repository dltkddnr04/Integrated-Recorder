// Package interaction holds generic adapter interaction progress without
// assigning platform-specific meaning to message types or fields.
package interaction

import (
	"errors"
	"sync"
	"time"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
)

var ErrNotFound = errors.New("interaction not found")
var ErrTerminal = errors.New("interaction is terminal")

type State struct {
	ID        string                            `json:"id"`
	Status    string                            `json:"status"`
	Messages  []adapterproto.InteractionMessage `json:"messages"`
	UpdatedAt time.Time                         `json:"updated_at"`
}

type Tracker struct {
	mu     sync.RWMutex
	states map[string]State
}

func NewTracker() *Tracker { return &Tracker{states: map[string]State{}} }
func (t *Tracker) Apply(message adapterproto.InteractionMessage) (State, error) {
	if err := message.Validate(); err != nil {
		return State{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	state, ok := t.states[message.InteractionID]
	if !ok {
		state = State{ID: message.InteractionID, Status: "active", Messages: []adapterproto.InteractionMessage{}}
	} else if state.Status != "active" {
		return State{}, ErrTerminal
	}
	switch message.Type {
	case "complete":
		state.Status = "complete"
	case "error":
		state.Status = "error"
	default:
		state.Status = "active"
	}
	state.Messages = append(state.Messages, message)
	state.UpdatedAt = time.Now().UTC()
	t.states[state.ID] = state
	return clone(state), nil
}
func (t *Tracker) Get(id string) (State, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	state, ok := t.states[id]
	if !ok {
		return State{}, ErrNotFound
	}
	return clone(state), nil
}
func clone(state State) State {
	state.Messages = append([]adapterproto.InteractionMessage(nil), state.Messages...)
	return state
}
