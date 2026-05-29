// Package agent runs the COREMETRY_MODE=agent role: it claims automated
// runbook steps (http / javascript / bash) from the API, executes them in an
// isolated pod, and posts the result back. Arbitrary operator-authored code
// runs HERE — never in the api/ingest/worker roles — so the blast radius is a
// dedicated, non-root, restricted pod the operator controls. (v0.7.4)
package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// AutomatedStep is the minimal payload an agent needs to run one step. It is
// the agent-facing projection of a runbook StepState (kind + the kind's
// payload), decoupled from the chstore type so the agent package stays
// dependency-light.
type AutomatedStep struct {
	Kind      string            `json:"kind"` // http | javascript | bash
	URL       string            `json:"url,omitempty"`
	Method    string            `json:"method,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	Script    string            `json:"script,omitempty"`
	Command   string            `json:"command,omitempty"`
	TimeoutMs int               `json:"timeoutMs,omitempty"`
}

// StepResult is the outcome of executing one automated step.
type StepResult struct {
	Output string `json:"output"`          // stdout / return value / HTTP status+body (truncated)
	Error  string `json:"error,omitempty"` // empty = success
}

const (
	maxOutput      = 16 * 1024
	defaultTimeout = 10 * time.Second
	maxTimeout     = 5 * time.Minute
)

// Execute dispatches a step to the right executor. Unknown kinds return an
// error result (never panic) so a malformed step can't wedge the agent.
func Execute(ctx context.Context, s AutomatedStep) StepResult {
	to := stepTimeout(s.TimeoutMs)
	switch s.Kind {
	case "http":
		return executeHTTP(ctx, s, to)
	case "javascript":
		return executeJavaScript(s.Script, to)
	case "bash":
		return executeBash(ctx, s.Command, to)
	default:
		return StepResult{Error: fmt.Sprintf("agent cannot execute step kind %q", s.Kind)}
	}
}

func stepTimeout(ms int) time.Duration {
	if ms <= 0 {
		return defaultTimeout
	}
	d := time.Duration(ms) * time.Millisecond
	if d > maxTimeout {
		return maxTimeout
	}
	return d
}

func truncate(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + "\n…[truncated]"
}

func executeHTTP(ctx context.Context, s AutomatedStep, to time.Duration) StepResult {
	method := strings.ToUpper(strings.TrimSpace(s.Method))
	if method == "" {
		method = http.MethodGet
	}
	cctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	var bodyR io.Reader
	if s.Body != "" {
		bodyR = strings.NewReader(s.Body)
	}
	req, err := http.NewRequestWithContext(cctx, method, s.URL, bodyR)
	if err != nil {
		return StepResult{Error: err.Error()}
	}
	for k, v := range s.Headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return StepResult{Error: err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxOutput))
	res := StepResult{Output: truncate(fmt.Sprintf("HTTP %d %s\n%s", resp.StatusCode, resp.Status, string(b)))}
	if resp.StatusCode >= 400 {
		res.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return res
}

// executeJavaScript runs script in a goja VM. goja installs NO host bindings
// by default — there is no require / fs / net / process / os, so the script is
// a pure-ECMAScript compute sandbox that cannot touch the agent's filesystem,
// network, env, or the host process. The only escape we guard is unbounded
// run time, via a hard wall-clock Interrupt.
func executeJavaScript(script string, to time.Duration) StepResult {
	vm := goja.New()
	timer := time.AfterFunc(to, func() { vm.Interrupt("execution timeout") })
	defer timer.Stop()
	v, err := vm.RunString(script)
	if err != nil {
		return StepResult{Error: err.Error()}
	}
	out := ""
	if v != nil && !goja.IsUndefined(v) && !goja.IsNull(v) {
		out = v.String()
	}
	return StepResult{Output: truncate(out)}
}

func executeBash(ctx context.Context, command string, to time.Duration) StepResult {
	cctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	cmd := exec.CommandContext(cctx, "/bin/sh", "-c", command)
	out, err := cmd.CombinedOutput()
	res := StepResult{Output: truncate(string(out))}
	switch {
	case cctx.Err() == context.DeadlineExceeded:
		res.Error = "command timed out after " + to.String()
	case err != nil:
		res.Error = err.Error()
	}
	return res
}
