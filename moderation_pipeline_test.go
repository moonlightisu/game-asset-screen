package main

import (
	"context"
	"errors"
	"testing"
)

type stubModerator struct {
	flagged bool
	err     error
}

func (s stubModerator) Flagged(context.Context, Submission) (bool, error) {
	return s.flagged, s.err
}

type stubTransformer struct {
	id    string
	calls int
	err   error
}

func (s *stubTransformer) Resize(context.Context, Submission) (string, error) {
	s.calls++
	return s.id, s.err
}

func TestPipelineDecision(t *testing.T) {
	tests := []struct {
		name             string
		moderator        stubModerator
		transformerError error
		wantState        string
		wantImageID      string
		wantTransforms   int
		wantError        bool
	}{
		{name: "flagged image or caption stays quarantined", moderator: stubModerator{flagged: true}, wantState: "quarantined", wantTransforms: 0},
		{name: "approved submission is resized for publish", moderator: stubModerator{}, wantState: "ready", wantImageID: "img_game_42", wantTransforms: 1},
		{name: "moderation error stops processing", moderator: stubModerator{err: errors.New("moderation rejected input")}, wantTransforms: 0, wantError: true},
		{name: "resize error does not mark ready", moderator: stubModerator{}, transformerError: errors.New("resize failed"), wantTransforms: 1, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transformer := &stubTransformer{id: "img_game_42", err: tt.transformerError}
			pipeline := NewPipeline(tt.moderator, transformer)
			got, err := pipeline.Screen(context.Background(), Submission{ID: "event-42", Caption: "victory pose", Image: []byte("jpeg")})
			if (err != nil) != tt.wantError {
				t.Fatalf("Screen() error = %v, wantError %v", err, tt.wantError)
			}
			if got.State != tt.wantState || got.ImageID != tt.wantImageID {
				t.Fatalf("Screen() = state %q, image %q; want %q, %q", got.State, got.ImageID, tt.wantState, tt.wantImageID)
			}
			if transformer.calls != tt.wantTransforms {
				t.Fatalf("resize calls = %d, want %d", transformer.calls, tt.wantTransforms)
			}
		})
	}
}
