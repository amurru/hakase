package channel

import (
	"context"
	"encoding/json"

	"amurru/hakase/internal/web/sse"
)

// Router subscribes to the bridge's global session ("") — where the web
// gates and notifier publish approval/clarify prompts and task/cron/
// delegation lifecycle — and routes each event to the registered transports'
// PushHandlers. It never blocks the bridge: the subscription channel is
// drained promptly and slow transports drop on their own.
type Router struct {
	bridge *sse.EventBridge
	pushes []PushHandler
	log    LogFunc
}

// NewRouter creates a router over the bridge.
func NewRouter(bridge *sse.EventBridge, pushes []PushHandler, log LogFunc) *Router {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Router{bridge: bridge, pushes: pushes, log: log}
}

// Run subscribes and dispatches until ctx is cancelled.
func (r *Router) Run(ctx context.Context) {
	subID, ch := r.bridge.SubscribeEvents("")
	defer r.bridge.UnsubscribeEvents("", subID)
	r.log("channels: router subscribed to bridge events")
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			r.dispatch(ev)
		}
	}
}

// Bridge payload shapes (must match sse.EventBridge senders).
type (
	approvalPayload struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
		Tool      string `json:"tool"`
		Risk      string `json:"risk"`
		Reason    string `json:"reason"`
		Command   string `json:"command"`
	}
	clarifyPayload struct {
		ID          string   `json:"id"`
		SessionID   string   `json:"session_id"`
		Question    string   `json:"question"`
		Choices     []string `json:"choices"`
		MultiSelect bool     `json:"multi_select"`
	}
	cronPayload struct {
		JobID      string `json:"job_id"`
		Name       string `json:"name"`
		Status     string `json:"status"`
		Summary    string `json:"summary"`
		OutputPath string `json:"output_path"`
	}
	taskPayload struct {
		Task   taskInfo `json:"task"`
		Action string   `json:"action"`
	}
	taskInfo struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	delegationPayload struct {
		TaskID  string `json:"task_id"`
		Agent   string `json:"agent"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}
)

// dispatch parses one event and forwards the user-relevant subset. Verbose
// progress statuses (started/running/thinking/...) are already visible in a
// running chat's status message and are not pushed.
func (r *Router) dispatch(ev sse.Event) {
	switch ev.Name {
	case "approval":
		var p approvalPayload
		if json.Unmarshal(ev.Data, &p) != nil || p.ID == "" {
			return
		}
		for _, push := range r.pushes {
			push.ApprovalPrompt(p.SessionID, p.ID, p.Tool, p.Risk, p.Reason, p.Command)
		}
	case "clarify":
		var p clarifyPayload
		if json.Unmarshal(ev.Data, &p) != nil || p.ID == "" {
			return
		}
		for _, push := range r.pushes {
			push.ClarifyPrompt(p.SessionID, p.ID, p.Question, p.Choices, p.MultiSelect)
		}
	case "cron":
		var p cronPayload
		if json.Unmarshal(ev.Data, &p) != nil {
			return
		}
		switch p.Status {
		case "completed", "failed", "silent":
			for _, push := range r.pushes {
				push.CronEvent(p.Status, p.JobID, p.Name, p.Summary, p.OutputPath)
			}
		}
	case "task":
		var p taskPayload
		if json.Unmarshal(ev.Data, &p) != nil {
			return
		}
		switch p.Action {
		case "completed", "failed":
			for _, push := range r.pushes {
				push.TaskEvent(p.Action, p.Task.ID, p.Task.Title, p.Task.Status)
			}
		}
	case "delegation":
		var p delegationPayload
		if json.Unmarshal(ev.Data, &p) != nil {
			return
		}
		switch p.Status {
		case "completed", "failed", "timed_out":
			for _, push := range r.pushes {
				push.DelegationEvent(p.Status, p.TaskID, p.Agent, p.Message)
			}
		}
	}
}
