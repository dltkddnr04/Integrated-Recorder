package interaction

import (
	"errors"
	"testing"

	"github.com/dltkddnr04/integrated-recorder/internal/adapterproto"
)

func TestInteractionGenericStateTransitions(t *testing.T) {
	tracker := NewTracker()
	prompt := adapterproto.InteractionMessage{Type: "secret_prompt", InteractionID: "opaque-id", Fields: []adapterproto.InteractionField{{Key: "value", Control: "secret", Label: "Value"}}}
	state, err := tracker.Apply(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "active" || len(state.Messages) != 1 {
		t.Fatalf("state = %#v", state)
	}
	state, err = tracker.Apply(adapterproto.InteractionMessage{Type: "status", InteractionID: "opaque-id", Message: "waiting"})
	if err != nil || len(state.Messages) != 2 {
		t.Fatalf("progress state = %#v, %v", state, err)
	}
	state, err = tracker.Apply(adapterproto.InteractionMessage{Type: "complete", InteractionID: "opaque-id"})
	if err != nil || state.Status != "complete" {
		t.Fatalf("terminal state = %#v, %v", state, err)
	}
	if _, err = tracker.Apply(adapterproto.InteractionMessage{Type: "status", InteractionID: "opaque-id"}); !errors.Is(err, ErrTerminal) {
		t.Fatalf("terminal update error = %v", err)
	}
}
