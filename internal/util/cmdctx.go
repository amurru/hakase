package util

import (
	"context"
	"os/exec"
	"path/filepath"
)

// cmdDoneWatcher mirrors the cancellation behavior os/exec.CommandContext
// wires into a command, without constructing the command through it: when ctx
// is done before or during the run, the process is killed. It returns a stop
// function that must be called (deferred) once the run finished so the
// watcher goroutine never leaks. Killing a process that already exited is a
// no-op, and a not-yet-started process (cmd.Process == nil) is skipped.
func cmdDoneWatcher(ctx context.Context, cmd *exec.Cmd) (stop func()) {
	if ctx == nil || ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-done:
		}
	}()
	return func() { close(done) }
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

// RunContext runs cmd and returns the result, preserving
// os/exec.CommandContext semantics: a context that is already done prevents
// the run, a context finishing mid-run kills the process and yields ctx.Err,
// and any other result is cmd.Run's own error.
func RunContext(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctxPreDone(ctx); err != nil {
		return err
	}
	if err := resolveCmdPath(cmd); err != nil {
		return err
	}
	stop := cmdDoneWatcher(ctx, cmd)
	defer stop()
	return ctxErrorOr(ctx, cmd.Run())
}

// CombinedOutputContext is RunContext for cmd.CombinedOutput: it runs the
// command and returns its combined output, mapping cancellation to ctx.Err
// like os/exec.CommandContext does.
func CombinedOutputContext(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if err := ctxPreDone(ctx); err != nil {
		return nil, err
	}
	if err := resolveCmdPath(cmd); err != nil {
		return nil, err
	}
	stop := cmdDoneWatcher(ctx, cmd)
	defer stop()
	out, err := cmd.CombinedOutput()
	return out, ctxErrorOr(ctx, err)
}
