package util

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
)

// cancelWatcher kills cmd once it has started if ctx is done, mirroring
// os/exec.CommandContext's cancellation without constructing the command
// through it. Kill/stop ordering is synchronized so the watcher can never read
// cmd.Process before cmd.Start has assigned it: the watcher goroutine only
// touches the process after the started channel closes (which the caller does
// right after Start returns), and the stop channel unblocks it when the run
// finishes or Start fails.
type cancelWatcher struct {
	ctx       context.Context
	cmd       *exec.Cmd
	mu        sync.Mutex
	startedCh chan struct{}
	done      chan struct{}
}

func newCancelWatcher(ctx context.Context, cmd *exec.Cmd) *cancelWatcher {
	w := &cancelWatcher{
		ctx:       ctx,
		cmd:       cmd,
		startedCh: make(chan struct{}),
		done:      make(chan struct{}),
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-w.done:
			return
		}
		// Context finished: wait until the process exists (or the run is torn
		// down) before killing, so Process is never read during Start.
		select {
		case <-w.startedCh:
		case <-w.done:
			return
		}
		w.mu.Lock()
		if w.cmd.Process != nil {
			_ = w.cmd.Process.Kill()
		}
		w.mu.Unlock()
	}()
	return w
}

// started marks the process as started and lets the watcher kill it. Call
// immediately after cmd.Start succeeds; the channel close happens after Start
// has assigned cmd.Process, so the watcher observes a fully initialized
// process.
func (w *cancelWatcher) started() {
	close(w.startedCh)
}

// stop ends the watcher's lifetime. Call exactly once (deferred) after the
// command has been reaped or failed to start.
func (w *cancelWatcher) stop() {
	close(w.done)
}

// ctxErrorOr maps a failed run back to ctx.Err when the context finished, so
// callers observe the same error identity they would get from
// os/exec.CommandContext (context.DeadlineExceeded / context.Canceled).
func ctxErrorOr(ctx context.Context, err error) error {
	if err != nil && ctx != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
	}
	return err
}

// ctxPreDone returns ctx.Err when the context already finished; a nil or
// never-canceled context yields nil.
func ctxPreDone(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// resolveCmdPath mirrors what os/exec.Command does at construction: a bare
// executable name is resolved through PATH (never the working directory) and
// the absolute result stored back on cmd.Path. Commands built with absolute
// paths are untouched. Resolution errors surface as run errors, exactly like
// Command's deferred lookup failure would at Start.
func resolveCmdPath(cmd *exec.Cmd) error {
	if cmd.Path == "" || filepath.Base(cmd.Path) != cmd.Path {
		return nil
	}
	lp, err := exec.LookPath(cmd.Path)
	if err != nil {
		return err
	}
	cmd.Path = lp
	return nil
}

// startWithCancel resolves cmd's path, arms the watcher (when ctx is
// cancellable), and starts cmd. On success it marks the process started so an
// already-done context (canceled during the Start window) still terminates the
// freshly started process. Returns a nil watcher when there is nothing to
// cancel, so callers may skip stop/started.
func startWithCancel(ctx context.Context, cmd *exec.Cmd) (*cancelWatcher, error) {
	if err := resolveCmdPath(cmd); err != nil {
		return nil, err
	}
	var w *cancelWatcher
	if ctx != nil && ctx.Done() != nil {
		w = newCancelWatcher(ctx, cmd)
	}
	if err := cmd.Start(); err != nil {
		if w != nil {
			w.stop()
		}
		return nil, err
	}
	if w != nil {
		w.started()
	}
	return w, nil
}

// RunContext runs cmd and returns the result, preserving
// os/exec.CommandContext semantics: a context that is already done prevents
// the run, a context finishing mid-run kills the process and yields ctx.Err,
// and any other result is cmd.Run's own error. Cancellation during the Start
// window kills the started process rather than leaking it.
func RunContext(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctxPreDone(ctx); err != nil {
		return err
	}
	w, err := startWithCancel(ctx, cmd)
	if err != nil {
		return ctxErrorOr(ctx, err)
	}
	if w != nil {
		defer w.stop()
	}
	return ctxErrorOr(ctx, cmd.Wait())
}

// CombinedOutputContext is RunContext for cmd.CombinedOutput: it runs the
// command and returns its combined output, mapping cancellation to ctx.Err
// like os/exec.CommandContext does. Like os/exec.Cmd.CombinedOutput it errors
// when the caller pre-configured Stdout or Stderr.
func CombinedOutputContext(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if err := ctxPreDone(ctx); err != nil {
		return nil, err
	}
	if cmd.Stdout != nil {
		return nil, errors.New("exec: Stdout already set")
	}
	if cmd.Stderr != nil {
		return nil, errors.New("exec: Stderr already set")
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	w, err := startWithCancel(ctx, cmd)
	if err != nil {
		return nil, ctxErrorOr(ctx, err)
	}
	if w != nil {
		defer w.stop()
	}
	err = cmd.Wait()
	return buf.Bytes(), ctxErrorOr(ctx, err)
}
