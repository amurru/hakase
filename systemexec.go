package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// runningProcess tracks the live state of a single spawned system process.
// The output buffer and state fields are guarded by mu so they can be read
// while the process is still collecting output in a background goroutine.
type runningProcess struct {
	id        int64
	cmd       *exec.Cmd
	cmdName   string
	args      []string
	startTime time.Time
	doneCh    chan struct{}

	mu       sync.Mutex
	out      bytes.Buffer
	exitCode int
	timedOut bool
	canceled bool
	finished bool
}

// newRunningProcess creates a runningProcess with sane defaults.
func newRunningProcess(id int64, cmd *exec.Cmd, cmdName string, args []string) *runningProcess {
	return &runningProcess{
		id:        id,
		cmd:       cmd,
		cmdName:   cmdName,
		args:      args,
		startTime: time.Now(),
		doneCh:    make(chan struct{}),
		exitCode:  -1,
	}
}

// appendOutput appends newly captured stdout/stderr bytes to the combined buffer.
func (rp *runningProcess) appendOutput(data []byte) {
	if len(data) == 0 {
		return
	}
	rp.mu.Lock()
	defer rp.mu.Unlock()
	_, _ = rp.out.Write(data)
}

// outputString returns all combined output collected so far.
func (rp *runningProcess) outputString() string {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.out.String()
}

// stateSnapshot atomically reads the mutable process state fields.
func (rp *runningProcess) stateSnapshot() (exitCode int, timedOut, canceled, finished bool) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return rp.exitCode, rp.timedOut, rp.canceled, rp.finished
}

// isTimedOut reports whether the process was killed due to a timeout.
func (rp *runningProcess) isTimedOut() bool {
	_, timedOut, _, _ := rp.stateSnapshot()
	return timedOut
}

// markTimedOut records that the process was killed by a timeout.
func (rp *runningProcess) markTimedOut() {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.timedOut = true
}

// markCanceled records that the process was explicitly killed by system_exec_kill.
func (rp *runningProcess) markCanceled() {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	rp.canceled = true
}

// setFinished records the exit code, marks the process finished, and closes
// doneCh so that any goroutine waiting on termination is released.
func (rp *runningProcess) setFinished(exitCode int) {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	if rp.finished {
		return
	}
	rp.finished = true
	rp.exitCode = exitCode
	close(rp.doneCh)
}

// waitFinished blocks until the process has been reaped by its wait goroutine.
func (rp *runningProcess) waitFinished() {
	<-rp.doneCh
}

// processOutputWriter funnels combined stdout+stderr into a runningProcess.
type processOutputWriter struct {
	rp *runningProcess
}

// Write implements io.Writer.
func (w processOutputWriter) Write(p []byte) (int, error) {
	w.rp.appendOutput(p)
	return len(p), nil
}

// systemExecManager is a concurrency-safe in-process registry of background
// system processes, owned once per setupRunner call and captured by all tool
// closures.
type systemExecManager struct {
	mu     sync.Mutex
	procs  map[int64]*runningProcess
	nextID int64
}

// allocateID reserves a unique process ID without registering a process.
func (m *systemExecManager) allocateID() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	return m.nextID
}

// register stores a newly started background process in the registry and
// returns it with a freshly allocated ID.
func (m *systemExecManager) register(cmd *exec.Cmd, cmdName string, args []string) *runningProcess {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	rp := newRunningProcess(m.nextID, cmd, cmdName, args)
	m.procs[rp.id] = rp
	return rp
}

// get looks up a registered background process by ID.
func (m *systemExecManager) get(id int64) (*runningProcess, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rp, ok := m.procs[id]
	return rp, ok
}

// remove deletes a process from the registry.
func (m *systemExecManager) remove(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.procs, id)
}

// snapshot returns a copy of all registered processes for listing.
func (m *systemExecManager) snapshot() []*runningProcess {
	m.mu.Lock()
	defer m.mu.Unlock()
	procs := make([]*runningProcess, 0, len(m.procs))
	for _, rp := range m.procs {
		procs = append(procs, rp)
	}
	return procs
}

// buildExecCommand constructs an exec.Cmd for a system command. The spawned
// process always inherits the agent process's environment (cmd.Env =
// os.Environ()) and any input env map is merged on top (input overrides).
func buildExecCommand(ctx agent.Context, command string, args []string, workingDir string, env map[string]string) (*exec.Cmd, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("command must not be empty")
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if workingDir != "" {
		cmd.Dir = workingDir
	}
	return cmd, nil
}

// SystemExecInput is the input schema for the synchronous system_exec tool.
type SystemExecInput struct {
	Command        string            `json:"command"            doc:"The system command or executable to run on the host machine (must be non-empty)"`
	Args           []string          `json:"args,omitempty"     doc:"Optional list of arguments passed to the command"`
	WorkingDir     string            `json:"working_dir,omitempty" doc:"Optional working directory for the command; defaults to the agent process working directory"`
	Env            map[string]string `json:"env,omitempty"      doc:"Optional environment variables merged over the agent process environment; these override inherited values"`
	TimeoutSeconds float64           `json:"timeout_seconds,omitempty" doc:"Optional timeout in seconds; the command is killed if it exceeds this duration"`
	MergeOutput    *bool             `json:"merge_output,omitempty" doc:"Combine stdout and stderr into a single output string (defaults to true when omitted)"`
}

// SystemExecOutput is the output schema for the synchronous system_exec tool.
type SystemExecOutput struct {
	ProcessID  int64  `json:"process_id"  doc:"The unique process ID assigned to this invocation"`
	ExitCode   int    `json:"exit_code"   doc:"The exit code of the command, or -1 if it was killed, timed out, or failed to start"`
	Stdout     string `json:"stdout"      doc:"Standard output captured from the command"`
	Stderr     string `json:"stderr"      doc:"Standard error captured from the command"`
	Output     string `json:"output"      doc:"Combined stdout and stderr output when merge_output is enabled"`
	TimedOut   bool   `json:"timed_out"   doc:"True if the command was killed because it exceeded the timeout"`
	DurationMs int64  `json:"duration_ms" doc:"Wall-clock duration of the command in milliseconds"`
}

// SystemExecStartInput is the input schema for the asynchronous system_exec_start tool.
type SystemExecStartInput struct {
	Command    string            `json:"command"        doc:"The system command or executable to start in the background (must be non-empty)"`
	Args       []string          `json:"args,omitempty" doc:"Optional list of arguments passed to the command"`
	WorkingDir string            `json:"working_dir,omitempty" doc:"Optional working directory for the command; defaults to the agent process working directory"`
	Env        map[string]string `json:"env,omitempty"  doc:"Optional environment variables merged over the agent process environment; these override inherited values"`
}

// SystemExecStartOutput is the output schema for the asynchronous system_exec_start tool.
type SystemExecStartOutput struct {
	ProcessID int64  `json:"process_id" doc:"The registered process ID used to poll, inspect, or kill the background process"`
	Started   bool   `json:"started"    doc:"True if the background process was successfully started"`
	Message   string `json:"message"    doc:"Human-readable status message describing the result"`
}

// SystemExecStatusInput is the input schema for the system_exec_status tool.
type SystemExecStatusInput struct {
	ProcessID int64 `json:"process_id" doc:"The process ID of the background process to inspect"`
}

// SystemExecStatusOutput is the output schema for the system_exec_status tool.
type SystemExecStatusOutput struct {
	ProcessID  int64  `json:"process_id"  doc:"The process ID that was inspected"`
	Running    bool   `json:"running"     doc:"True if the background process is still running"`
	Finished   bool   `json:"finished"    doc:"True if the background process has exited"`
	ExitCode   int    `json:"exit_code"   doc:"The exit code of the process, or -1 if it is still running or was killed"`
	Output     string `json:"output"      doc:"Combined stdout and stderr output collected so far"`
	DurationMs int64  `json:"duration_ms" doc:"Wall-clock duration since the process started, in milliseconds"`
}

// SystemExecKillInput is the input schema for the system_exec_kill tool.
type SystemExecKillInput struct {
	ProcessID int64 `json:"process_id" doc:"The process ID of the background process to terminate"`
}

// SystemExecKillOutput is the output schema for the system_exec_kill tool.
type SystemExecKillOutput struct {
	ProcessID int64  `json:"process_id" doc:"The process ID that was targeted"`
	Killed    bool   `json:"killed"     doc:"True if the process was terminated by this call"`
	Message   string `json:"message"    doc:"Human-readable status message describing the result"`
}

// SystemExecListInput is the input schema for the system_exec_list tool.
type SystemExecListInput struct{}

// SystemExecProcessInfo describes the live state of a registered background process.
type SystemExecProcessInfo struct {
	ProcessID  int64    `json:"process_id"  doc:"The registered process ID"`
	Command    string   `json:"command"     doc:"The command or executable that was started"`
	Args       []string `json:"args"        doc:"The arguments the command was started with"`
	Running    bool     `json:"running"     doc:"True if the process is still running"`
	ExitCode   int      `json:"exit_code"   doc:"The exit code of the process, or -1 if it is still running or was killed"`
	DurationMs int64    `json:"duration_ms" doc:"Wall-clock duration since the process started, in milliseconds"`
}

// SystemExecListOutput is the output schema for the system_exec_list tool.
type SystemExecListOutput struct {
	Processes []SystemExecProcessInfo `json:"processes" doc:"All registered background processes with their live state"`
}

// createSystemExecTools builds the five-tool host system execution toolset.
// All tools share a single systemExecManager instance so background processes
// started by system_exec_start can be polled, listed, and killed.
func createSystemExecTools(log LogFunc) ([]tool.Tool, error) {
	m := &systemExecManager{
		procs: make(map[int64]*runningProcess),
	}

	// system_exec: synchronous fire-and-wait execution.
	execTool, err := functiontool.New(functiontool.Config{
		Name:        "system_exec",
		Description: "Runs a system command or executable directly on the host machine synchronously and waits for it to finish or time out. Not routed through the Python interpreter.",
	}, func(ctx agent.Context, input SystemExecInput) (SystemExecOutput, error) {
		start := time.Now()
		procID := m.allocateID()
		cmd, err := buildExecCommand(ctx, input.Command, input.Args, input.WorkingDir, input.Env)
		if err != nil {
			return SystemExecOutput{ProcessID: procID}, err
		}

		rp := newRunningProcess(procID, cmd, input.Command, input.Args)
		outWriter := processOutputWriter{rp: rp}
		var stdoutBuf, stderrBuf bytes.Buffer
		cmd.Stdout = io.MultiWriter(&stdoutBuf, outWriter)
		cmd.Stderr = io.MultiWriter(&stderrBuf, outWriter)

		if log != nil {
			log(fmt.Sprintf("⚡ [system_exec] Running: %s %s", input.Command, strings.Join(input.Args, " ")))
		}

		if err := cmd.Start(); err != nil {
			return SystemExecOutput{
				ProcessID:  procID,
				ExitCode:   -1,
				TimedOut:   false,
				DurationMs: time.Since(start).Milliseconds(),
			}, fmt.Errorf("failed to start command %q: %w", input.Command, err)
		}

		var timeoutTimer *time.Timer
		if input.TimeoutSeconds > 0 {
			timeoutTimer = time.AfterFunc(
				time.Duration(input.TimeoutSeconds*float64(time.Second)),
				func() {
					rp.markTimedOut()
					if cmd.Process != nil {
						_ = cmd.Process.Kill()
					}
				},
			)
			defer timeoutTimer.Stop()
		}

		runErr := cmd.Wait()
		rp.setFinished(-1)

		out := SystemExecOutput{
			ProcessID:  procID,
			ExitCode:   -1,
			TimedOut:   rp.isTimedOut(),
			DurationMs: time.Since(start).Milliseconds(),
			Stdout:     stdoutBuf.String(),
			Stderr:     stderrBuf.String(),
		}

		var exitErr *exec.ExitError
		switch {
		case runErr == nil:
			out.ExitCode = 0
		case errors.As(runErr, &exitErr):
			out.ExitCode = exitErr.ExitCode()
		case !out.TimedOut:
			return out, fmt.Errorf("failed to run command %q: %w", input.Command, runErr)
		}

		mergeOutput := true
		if input.MergeOutput != nil {
			mergeOutput = *input.MergeOutput
		}
		if mergeOutput {
			out.Output = rp.outputString()
		}

		if log != nil {
			log(fmt.Sprintf("✅ [system_exec] Process #%d finished (exit %d, %d ms)", procID, out.ExitCode, out.DurationMs))
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}

	// system_exec_start: detached background execution registered in the registry.
	startTool, err := functiontool.New(functiontool.Config{
		Name:        "system_exec_start",
		Description: "Starts a system command or executable on the host machine in the background, registers it in the process registry, and returns immediately with a process ID for later polling with system_exec_status, killing with system_exec_kill, or listing with system_exec_list.",
	}, func(ctx agent.Context, input SystemExecStartInput) (SystemExecStartOutput, error) {
		cmd, err := buildExecCommand(ctx, input.Command, input.Args, input.WorkingDir, input.Env)
		if err != nil {
			return SystemExecStartOutput{Started: false, Message: err.Error()}, err
		}

		rp := m.register(cmd, input.Command, input.Args)
		outWriter := processOutputWriter{rp: rp}
		cmd.Stdout = outWriter
		cmd.Stderr = outWriter

		if log != nil {
			log(fmt.Sprintf("🚀 [system_exec] Starting background process #%d: %s %s", rp.id, input.Command, strings.Join(input.Args, " ")))
		}

		if err := cmd.Start(); err != nil {
			m.remove(rp.id)
			rp.setFinished(-1)
			return SystemExecStartOutput{
				ProcessID: rp.id,
				Started:   false,
				Message:   fmt.Sprintf("failed to start command: %v", err),
			}, err
		}

		go func() {
			exitCode := 0
			if err := cmd.Wait(); err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					exitCode = exitErr.ExitCode()
				} else {
					exitCode = -1
				}
			}
			rp.setFinished(exitCode)
			if log != nil {
				log(fmt.Sprintf("✅ [system_exec] Background process #%d exited with code %d", rp.id, exitCode))
			}
		}()

		return SystemExecStartOutput{
			ProcessID: rp.id,
			Started:   true,
			Message:   fmt.Sprintf("started background process #%d: %s", rp.id, input.Command),
		}, nil
	})
	if err != nil {
		return nil, err
	}

	// system_exec_status: poll the live state of a background process.
	statusTool, err := functiontool.New(functiontool.Config{
		Name:        "system_exec_status",
		Description: "Returns the current live state (running, exit code, combined output collected so far, duration) of a background process previously started with system_exec_start.",
	}, func(ctx agent.Context, input SystemExecStatusInput) (SystemExecStatusOutput, error) {
		rp, ok := m.get(input.ProcessID)
		if !ok {
			return SystemExecStatusOutput{ProcessID: input.ProcessID}, fmt.Errorf("no such process: %d", input.ProcessID)
		}
		exitCode, _, _, finished := rp.stateSnapshot()
		return SystemExecStatusOutput{
			ProcessID:  rp.id,
			Running:    !finished,
			Finished:   finished,
			ExitCode:   exitCode,
			Output:     rp.outputString(),
			DurationMs: time.Since(rp.startTime).Milliseconds(),
		}, nil
	})
	if err != nil {
		return nil, err
	}

	// system_exec_kill: terminate and reap a background process.
	killTool, err := functiontool.New(functiontool.Config{
		Name:        "system_exec_kill",
		Description: "Terminates a background process previously started with system_exec_start (SIGKILL), reaps it, and removes it from the process registry.",
	}, func(ctx agent.Context, input SystemExecKillInput) (SystemExecKillOutput, error) {
		rp, ok := m.get(input.ProcessID)
		if !ok {
			return SystemExecKillOutput{ProcessID: input.ProcessID, Killed: false, Message: "no such process"}, fmt.Errorf("no such process: %d", input.ProcessID)
		}

		_, _, _, alreadyFinished := rp.stateSnapshot()
		rp.markCanceled()
		if !alreadyFinished && rp.cmd.Process != nil {
			_ = rp.cmd.Process.Kill()
		}
		rp.waitFinished()
		m.remove(input.ProcessID)

		if log != nil {
			log(fmt.Sprintf("🛑 [system_exec] Killed background process #%d", input.ProcessID))
		}
		if alreadyFinished {
			return SystemExecKillOutput{
				ProcessID: input.ProcessID,
				Killed:    false,
				Message:   "process had already finished",
			}, nil
		}
		return SystemExecKillOutput{
			ProcessID: input.ProcessID,
			Killed:    true,
			Message:   "process terminated",
		}, nil
	})
	if err != nil {
		return nil, err
	}

	// system_exec_list: list all registered background processes.
	listTool, err := functiontool.New(functiontool.Config{
		Name:        "system_exec_list",
		Description: "Lists all registered background system processes started by system_exec_start and their live state (running, exit code, duration).",
	}, func(ctx agent.Context, _ SystemExecListInput) (SystemExecListOutput, error) {
		procs := m.snapshot()
		infos := make([]SystemExecProcessInfo, 0, len(procs))
		for _, rp := range procs {
			exitCode, _, _, finished := rp.stateSnapshot()
			infos = append(infos, SystemExecProcessInfo{
				ProcessID:  rp.id,
				Command:    rp.cmdName,
				Args:       rp.args,
				Running:    !finished,
				ExitCode:   exitCode,
				DurationMs: time.Since(rp.startTime).Milliseconds(),
			})
		}
		return SystemExecListOutput{Processes: infos}, nil
	})
	if err != nil {
		return nil, err
	}

	return []tool.Tool{execTool, startTool, statusTool, killTool, listTool}, nil
}
