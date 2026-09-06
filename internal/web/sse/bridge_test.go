package sse

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
)

func TestEventBridgeStreamContent(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.SendStreamContent("sess-1", "Hello", "world")

	select {
	case data := <-ch:
		validateSSE(t, data, "stream", func(payload map[string]any) {
			if payload["content"] != "Hello" {
				t.Errorf("expected content='Hello', got %v", payload["content"])
			}
			if payload["thinking"] != "world" {
				t.Errorf("expected thinking='world', got %v", payload["thinking"])
			}
		})
	default:
		t.Fatal("expected event on channel")
	}
}

func TestEventBridgeLog(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.SendLog("sess-1", "tool call: foo()")

	select {
	case data := <-ch:
		validateSSE(t, data, "log", func(payload map[string]any) {
			if payload["line"] != "tool call: foo()" {
				t.Errorf("expected line='tool call: foo()', got %v", payload["line"])
			}
		})
	default:
		t.Fatal("expected event on channel")
	}
}

func TestEventBridgeDone(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.SendDone("sess-1")

	select {
	case data := <-ch:
		if !bytes.HasPrefix(data, []byte("event: done\n")) {
			t.Errorf("expected 'event: done', got %s", data)
		}
	default:
		t.Fatal("expected event on channel")
	}
}

func TestEventBridgeApprovalPrompt(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.SendApprovalPrompt("sess-1", "sess-1", "apr-1", "system_exec", "high", "needs confirmation", "rm -rf /")

	select {
	case data := <-ch:
		validateSSE(t, data, "approval", func(payload map[string]any) {
			if payload["id"] != "apr-1" {
				t.Errorf("expected id='apr-1', got %v", payload["id"])
			}
			if payload["tool"] != "system_exec" {
				t.Errorf("expected tool='system_exec', got %v", payload["tool"])
			}
			if payload["risk"] != "high" {
				t.Errorf("expected risk='high', got %v", payload["risk"])
			}
		})
	default:
		t.Fatal("expected event on channel")
	}
}

func TestEventBridgeClarifyPrompt(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.SendClarifyPrompt("sess-1", "sess-1", "clr-1", "What platform?", []string{"linux", "macos"}, false)

	select {
	case data := <-ch:
		validateSSE(t, data, "clarify", func(payload map[string]any) {
			if payload["id"] != "clr-1" {
				t.Errorf("expected id='clr-1', got %v", payload["id"])
			}
			if payload["question"] != "What platform?" {
				t.Errorf("expected question='What platform?', got %v", payload["question"])
			}
			choices, ok := payload["choices"].([]interface{})
			if !ok || len(choices) != 2 {
				t.Errorf("expected 2 choices, got %v", payload["choices"])
			}
		})
	default:
		t.Fatal("expected event on channel")
	}
}

func TestEventBridgeDelegation(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.SendDelegation("sess-1", "t-1", "web_researcher", "started", "Searching...")

	select {
	case data := <-ch:
		validateSSE(t, data, "delegation", func(payload map[string]any) {
			if payload["task_id"] != "t-1" {
				t.Errorf("expected task_id='t-1', got %v", payload["task_id"])
			}
			if payload["agent"] != "web_researcher" {
				t.Errorf("expected agent='web_researcher', got %v", payload["agent"])
			}
			if payload["status"] != "started" {
				t.Errorf("expected status='started', got %v", payload["status"])
			}
		})
	default:
		t.Fatal("expected event on channel")
	}
}

func TestEventBridgeCron(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.SendCron("sess-1", "job-1", "daily-digest", "completed", "summary", "/tmp/output")

	select {
	case data := <-ch:
		validateSSE(t, data, "cron", func(payload map[string]any) {
			if payload["job_id"] != "job-1" {
				t.Errorf("expected job_id='job-1', got %v", payload["job_id"])
			}
			if payload["name"] != "daily-digest" {
				t.Errorf("expected name='daily-digest', got %v", payload["name"])
			}
			if payload["status"] != "completed" {
				t.Errorf("expected status='completed', got %v", payload["status"])
			}
		})
	default:
		t.Fatal("expected event on channel")
	}
}

func TestEventBridgeTask(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	taskMap := map[string]any{
		"id": "t-1", "title": "Fix tests", "status": "in_progress",
	}
	b.SendTask("sess-1", taskMap, "updated")

	select {
	case data := <-ch:
		validateSSE(t, data, "task", func(payload map[string]any) {
			task := payload["task"].(map[string]interface{})
			if task["title"] != "Fix tests" {
				t.Errorf("expected title='Fix tests', got %v", task["title"])
			}
			if payload["action"] != "updated" {
				t.Errorf("expected action='updated', got %v", payload["action"])
			}
		})
	default:
		t.Fatal("expected event on channel")
	}
}

func TestEventBridgeUsage(t *testing.T) {
	b := NewEventBridge()
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	b.SendUsage("sess-1", 1234, 42)

	select {
	case data := <-ch:
		validateSSE(t, data, "usage", func(payload map[string]any) {
			if int(payload["tokens"].(float64)) != 1234 {
				t.Errorf("expected tokens=1234, got %v", payload["tokens"])
			}
			if int(payload["percent"].(float64)) != 42 {
				t.Errorf("expected percent=42, got %v", payload["percent"])
			}
		})
	default:
		t.Fatal("expected event on channel")
	}
}

func TestEventBridgeMultipleSubscribers(t *testing.T) {
	b := NewEventBridge()
	_, ch1 := b.Subscribe("sess-1")
	_, ch2 := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)
	defer b.Unsubscribe("sess-1", 2)

	b.SendDone("sess-1")

	// Both subscribers should receive the event.
	for i, ch := range []<-chan []byte{ch1, ch2} {
		select {
		case data := <-ch:
			if !bytes.HasPrefix(data, []byte("event: done\n")) {
				t.Errorf("subscriber %d: expected 'event: done', got %s", i+1, data)
			}
		default:
			t.Errorf("subscriber %d: expected event on channel", i+1)
		}
	}
}

func TestEventBridgeDifferentSessions(t *testing.T) {
	b := NewEventBridge()
	_, ch1 := b.Subscribe("sess-1")
	_, ch2 := b.Subscribe("sess-2")
	defer b.Unsubscribe("sess-1", 1)
	defer b.Unsubscribe("sess-2", 1)

	b.SendDone("sess-1")

	// Only sess-1 subscriber should get the event.
	select {
	case data := <-ch1:
		if !bytes.HasPrefix(data, []byte("event: done\n")) {
			t.Errorf("sess-1: expected 'event: done', got %s", data)
		}
	default:
		t.Fatal("sess-1: expected event on channel")
	}

	// sess-2 should not get the event.
	select {
	case <-ch2:
		t.Error("sess-2: should NOT have received event")
	default:
		// Expected: no event.
	}
}

func TestEventBridgeUnsubscribe(t *testing.T) {
	b := NewEventBridge()
	id, ch := b.Subscribe("sess-1")

	b.Unsubscribe("sess-1", id)

	// Verify channel is closed.
	_, ok := <-ch
	if ok {
		t.Error("channel should be closed after unsubscribe")
	}

	// Publish after unsubscribe should not panic.
	b.SendDone("sess-1")
}

func TestEventBridgeNonBlockingPublish(t *testing.T) {
	b := NewEventBridge()
	// Create a subscriber with a small buffer and fill it.
	_, ch := b.Subscribe("sess-1")
	defer b.Unsubscribe("sess-1", 1)

	// Fill the channel (buffer=256). After that, publishes should drop.
	for i := 0; i < 300; i++ {
		b.SendDone("sess-1")
	}

	// Drain a few events to confirm they arrived (not empty).
	count := 0
	drained := false
	for !drained {
		select {
		case <-ch:
			count++
		default:
			drained = true
		}
	}
	if count < 200 {
		t.Errorf("expected at least 200 events in buffer, got %d", count)
	}
}

func TestEventBridgeEventNotifierInterface(t *testing.T) {
	b := NewEventBridge()
	// Compile-time check that EventBridge implements EventNotifier.
	var _ interfaces.EventNotifier = b

	_, ch := b.Subscribe("")
	defer b.Unsubscribe("", 1)

	// TaskUpdate
	b.TaskUpdate("created", interfaces.TaskMeta{
		ID: "t-1", Title: "Test", Status: interfaces.TaskStatusPending,
	})
	<-ch

	// DelegationProgress
	b.DelegationProgress("started", "t-2", "code_interpreter", "Running tests")
	<-ch

	// CronJobEvent
	b.CronJobEvent("started", "cron-1", "daily", "", "")
	<-ch
}

func TestPingComment(t *testing.T) {
	ping := PingComment()
	if string(ping) != ": ping\n\n" {
		t.Errorf("expected ': ping\\n\\n', got %q", ping)
	}
}

func TestSSEError(t *testing.T) {
	err := SSEError("something went wrong")
	if !bytes.HasPrefix(err, []byte("event: error\n")) {
		t.Errorf("expected 'event: error', got %s", err)
	}
}

func TestFormatSSE(t *testing.T) {
	result := string(formatSSE("test", []byte(`{"key":"value"}`)))
	if !strings.HasPrefix(result, "event: test\ndata: {\"key\":\"value\"}\n\n") {
		t.Errorf("unexpected SSE format: %q", result)
	}
}

// validateSSE parses an SSE byte slice and runs the given validation on the
// parsed JSON payload.
func validateSSE(t *testing.T, data []byte, expectedEvent string, validate func(payload map[string]any)) {
	t.Helper()
	parts := bytes.SplitN(data, []byte("\n"), 3)
	if len(parts) < 3 {
		t.Fatalf("malformed SSE data: %s", data)
	}

	eventLine := string(parts[0])
	if !strings.HasPrefix(eventLine, "event: ") || strings.TrimPrefix(eventLine, "event: ") != expectedEvent {
		t.Errorf("expected event '%s', got %q", expectedEvent, eventLine)
	}

	dataLine := string(parts[1])
	if !strings.HasPrefix(dataLine, "data: ") {
		t.Errorf("expected data line, got %q", dataLine)
	}

	jsonStr := strings.TrimPrefix(dataLine, "data: ")
	var payload map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
		t.Fatalf("failed to parse data JSON: %v (raw: %s)", err, jsonStr)
	}
	validate(payload)
}
