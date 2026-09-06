package channel

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/channel/state"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/web/sse"

	"google.golang.org/adk/v2/runner"
)

// Deps carries everything the channel subsystem needs from the host process.
// The web/serve bootstrap fills it after SetupRunner; all fields except the
// bridge are transport inputs the Service re-exposes to channels.
type Deps struct {
	Bridge   *sse.EventBridge
	Runner   *runner.Runner
	Sessions *hakasesession.SessionService
	// Approval/Clarify resolve pending gate prompts by ID (first responder
	// wins against the web UI). The web gates implement both.
	Approval interfaces.ApprovalResponder
	Clarify  interfaces.ClarifyResponder
	// StatePath overrides the state file location (tests); empty = default.
	StatePath string
	// Log is the subsystem logger; nil falls back to the standard logger.
	Log LogFunc
}

// Service owns the channel subsystem: the shared state store, the per-chat
// run manager, the shared agent-turn driver, and the registered transports.
// Start runs the router and every channel; Stop cancels them and waits.
type Service struct {
	deps    Deps
	log     LogFunc
	store   *state.Store
	runs    *RunManager
	driver  *agentrun.Driver
	entries []entry

	cancel     context.CancelFunc
	registerMu sync.Mutex
	wg         sync.WaitGroup
	running    atomic.Bool
}

type entry struct {
	ch   Channel
	push PushHandler
}

// NewService opens the state store and prepares the subsystem. It does not
// start anything until Start.
func NewService(d Deps) (*Service, error) {
	logFn := d.Log
	if logFn == nil {
		logFn = func(format string, args ...any) { log.Printf("channels: "+format, args...) }
	}
	path := d.StatePath
	if path == "" {
		path = state.DefaultPath()
	}
	store, err := state.Open(path)
	if err != nil {
		return nil, err
	}
	return &Service{
		deps:   d,
		log:    logFn,
		store:  store,
		runs:   NewRunManager(),
		driver: agentrun.New(d.Runner, d.Sessions),
	}, nil
}

// Store exposes the shared state store (transports build their
// Authenticator against it).
func (s *Service) Store() *state.Store { return s.store }

// Runs exposes the per-chat run manager.
func (s *Service) Runs() *RunManager { return s.runs }

// Driver exposes the shared agent-turn driver.
func (s *Service) Driver() *agentrun.Driver { return s.driver }

// Log returns the subsystem logger.
func (s *Service) Log() LogFunc { return s.log }

// Sessions exposes the host session service.
func (s *Service) Sessions() *hakasesession.SessionService { return s.deps.Sessions }

// ApprovalResponder exposes the gate approval resolver (may be nil in
// non-web hosts).
func (s *Service) ApprovalResponder() interfaces.ApprovalResponder { return s.deps.Approval }

// ClarifyResponder exposes the gate clarify resolver (may be nil).
func (s *Service) ClarifyResponder() interfaces.ClarifyResponder { return s.deps.Clarify }

// Register adds a transport with its push handler. Must be called before
// Start.
func (s *Service) Register(ch Channel, push PushHandler) {
	s.registerMu.Lock()
	defer s.registerMu.Unlock()
	s.entries = append(s.entries, entry{ch: ch, push: push})
}

// Start launches the router and all registered channels. Non-blocking.
func (s *Service) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.running.Store(true)

	s.registerMu.Lock()
	entries := append([]entry(nil), s.entries...)
	s.registerMu.Unlock()

	pushes := make([]PushHandler, 0, len(entries))
	for _, e := range entries {
		pushes = append(pushes, e.push)
	}
	router := NewRouter(s.deps.Bridge, pushes, s.log)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		router.Run(ctx)
	}()

	for _, e := range entries {
		e := e
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				if r := recover(); r != nil {
					s.log("channel %s crashed: %v", e.ch.Name(), r)
				}
			}()
			s.log("channel %s starting", e.ch.Name())
			if err := e.ch.Run(ctx); err != nil && ctx.Err() == nil {
				s.log("channel %s stopped with error: %v", e.ch.Name(), err)
			}
		}()
	}
}

// Stop cancels all channels and waits up to timeout for them to finish.
func (s *Service) Stop(timeout time.Duration) {
	if s.cancel != nil {
		s.cancel()
	}
	stopped := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(timeout):
		s.log("channels: stop timed out after %s", timeout)
	}
	s.running.Store(false)
}

// IsRunning reports whether the subsystem has been started and not yet
// stopped (the web API surfaces this as the channel's live status).
func (s *Service) IsRunning() bool { return s.running.Load() }
