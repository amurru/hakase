package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMessageJSONRoundTripNewFields(t *testing.T) {
	msg := Message{
		Role:      "agent",
		Content:   "hello world",
		Thinking:  "think",
		Timestamp: time.Now().UTC(),
		Tokens:    1234,
		Sequence:  7,
		InContext: false,
		Kind:      MessageKindToolResult,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Tokens != 1234 {
		t.Fatalf("Tokens = %d, want 1234", got.Tokens)
	}
	if got.Sequence != 7 {
		t.Fatalf("Sequence = %d, want 7", got.Sequence)
	}
	if got.InContext {
		t.Fatalf("InContext = true, want false (explicitly persisted)")
	}
	if got.Kind != MessageKindToolResult {
		t.Fatalf("Kind = %q, want %q", got.Kind, MessageKindToolResult)
	}
}

func TestMessageJSONRoundTripOmitEmpty(t *testing.T) {
	msg := Message{Role: "user", Content: "hi"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// tokens/sequence/kind are omitempty; in_context is always present.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if _, ok := raw["tokens"]; ok {
		t.Fatalf("tokens should be omitted when 0, got %v", raw["tokens"])
	}
	if _, ok := raw["in_context"]; !ok {
		t.Fatalf("in_context must always be present")
	}
}

func TestSequenceIncrementsPerMessage(t *testing.T) {
	s := NewSession("t")
	s.AddMessageWithMeta("user", "q1", "", 10, MessageKindText)
	s.AddMessageWithMeta("agent", "a1", "", 20, MessageKindText)
	s.AddMessageWithMeta("user", "q2", "", 30, MessageKindText)

	if len(s.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(s.Messages))
	}
	for i, msg := range s.Messages {
		if msg.Sequence != int64(i) {
			t.Fatalf("Messages[%d].Sequence = %d, want %d", i, msg.Sequence, i)
		}
	}
	if !s.Messages[0].InContext || !s.Messages[1].InContext || !s.Messages[2].InContext {
		t.Fatalf("new messages must be in-context by default")
	}
	if s.Messages[0].Kind != MessageKindText {
		t.Fatalf("default kind = %q, want %q", s.Messages[0].Kind, MessageKindText)
	}
}

func TestAddMessageDelegatesToWithMeta(t *testing.T) {
	s := NewSession("t")
	s.AddMessage("user", "q", "think")
	if len(s.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(s.Messages))
	}
	m := s.Messages[0]
	if m.Role != "user" || m.Content != "q" || m.Thinking != "think" {
		t.Fatalf("unexpected message: %+v", m)
	}
	if m.Kind != MessageKindText || m.Tokens != 0 || m.Sequence != 0 {
		t.Fatalf("metadata not defaulted: %+v", m)
	}
}

func TestSummaryMessageIDRoundTrip(t *testing.T) {
	s := NewSession("t")
	s.SummaryMessageID = "12"
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Session
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SummaryMessageID != "12" {
		t.Fatalf("SummaryMessageID = %q, want 12", got.SummaryMessageID)
	}
}

func TestNormalizeLegacyMessages(t *testing.T) {
	// Legacy file: no in_context field anywhere -> all messages become true.
	legacy := &Session{Messages: []Message{
		{Role: "user", Content: "a"},
		{Role: "agent", Content: "b"},
	}}
	normalizeLegacyMessages(legacy)
	for i, msg := range legacy.Messages {
		if !msg.InContext {
			t.Fatalf("legacy message %d not normalized to in-context", i)
		}
	}

	// Context-managed file: some messages out-of-context -> untouched.
	managed := &Session{Messages: []Message{
		{Role: "user", Content: "a", InContext: false},
		{Role: "agent", Content: "b", InContext: true},
	}}
	normalizeLegacyMessages(managed)
	if managed.Messages[0].InContext {
		t.Fatalf("out-of-context message must stay out-of-context")
	}
	if !managed.Messages[1].InContext {
		t.Fatalf("in-context message must stay in-context")
	}
}
