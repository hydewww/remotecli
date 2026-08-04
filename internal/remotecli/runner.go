package remotecli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type RunnerOptions struct {
	OpenCLIPath      string
	RunRoot          string
	Concurrency      int
	CommandTimeout   time.Duration
	MaxOutput        int
	MaxArtifactSize  int64
	MaxArtifactTotal int64
	Retention        time.Duration
}

type Runner struct {
	options  RunnerOptions
	sem      chan struct{}
	store    *runStore
	activeMu sync.Mutex
	active   map[string]context.CancelFunc
}

func resolveOpenCLIPath(configured string) (string, error) {
	value := strings.TrimSpace(configured)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("REMOTECLI_OPENCLI_BIN"))
	}
	if value == "" {
		value = "opencli"
	}
	if filepath.IsAbs(value) || strings.ContainsAny(value, `/\\`) {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", fmt.Errorf("resolve opencli executable %q: %w", value, err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return "", fmt.Errorf("opencli executable %q is not accessible: %w", value, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("opencli executable %q is a directory", value)
		}
		return absolute, nil
	}
	path, err := exec.LookPath(value)
	if err != nil {
		return "", fmt.Errorf("cannot find opencli executable %q in PATH: %w", value, err)
	}
	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve opencli executable %q: %w", value, err)
		}
	}
	return path, nil
}

func NewRunner(options RunnerOptions) (*Runner, error) {
	if options.Concurrency < 1 {
		options.Concurrency = DefaultConcurrency
	}
	if options.CommandTimeout <= 0 {
		options.CommandTimeout = DefaultCommandTimeout
	}
	if options.MaxOutput <= 0 {
		options.MaxOutput = DefaultMaxOutput
	}
	if options.MaxArtifactSize <= 0 {
		options.MaxArtifactSize = DefaultMaxArtifactSize
	}
	if options.MaxArtifactTotal <= 0 {
		options.MaxArtifactTotal = DefaultMaxArtifactTotal
	}
	if options.Retention <= 0 {
		options.Retention = DefaultRetention
	}
	if options.RunRoot == "" {
		root, err := defaultRunRoot()
		if err != nil {
			return nil, err
		}
		options.RunRoot = root
	}
	if err := os.MkdirAll(options.RunRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create run root: %w", err)
	}
	path, err := resolveOpenCLIPath(options.OpenCLIPath)
	if err != nil {
		return nil, err
	}
	options.OpenCLIPath = path
	return &Runner{
		options: options,
		sem:     make(chan struct{}, options.Concurrency),
		store:   newRunStore(options.Retention),
		active:  make(map[string]context.CancelFunc),
	}, nil
}

func (r *Runner) OpenCLIPath() string { return r.options.OpenCLIPath }

func (r *Runner) CancelAll() {
	r.activeMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(r.active))
	for _, cancel := range r.active {
		cancels = append(cancels, cancel)
	}
	r.activeMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) String() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String(), b.truncated
}

func (r *Runner) Execute(parent context.Context, request ExecuteRequest) (ExecuteResponse, error) {
	if len(request.Args) > 1024 {
		return ExecuteResponse{}, apiError("invalid_args", "too many OpenCLI arguments", "limit is 1024 arguments")
	}
	if request.Artifacts == false {
		// False is a supported opt-out; the client uses true by default.
	}
	runID, err := newID()
	if err != nil {
		return ExecuteResponse{}, fmt.Errorf("create run id: %w", err)
	}
	runContext, cancelRun := context.WithCancel(parent)
	defer cancelRun()
	r.activeMu.Lock()
	r.active[runID] = cancelRun
	r.activeMu.Unlock()
	defer func() {
		r.activeMu.Lock()
		delete(r.active, runID)
		r.activeMu.Unlock()
	}()
	runDir := filepath.Join(r.options.RunRoot, runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return ExecuteResponse{}, fmt.Errorf("create run directory: %w", err)
	}

	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-runContext.Done():
		_ = os.RemoveAll(runDir)
		return ExecuteResponse{}, runContext.Err()
	}

	seconds := r.options.CommandTimeout
	if request.TimeoutMS > 0 {
		requested := time.Duration(request.TimeoutMS) * time.Millisecond
		if requested < seconds {
			seconds = requested
		}
	}
	ctx, cancel := context.WithTimeout(runContext, seconds)
	defer cancel()

	cmd := buildOpenCLICommand(ctx, r.options.OpenCLIPath, request.Args)
	cmd.Dir = runDir
	cmd.Env = os.Environ()
	stdout := &limitedBuffer{limit: r.options.MaxOutput}
	stderr := &limitedBuffer{limit: r.options.MaxOutput}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	started := time.Now()
	err = cmd.Run()
	duration := time.Since(started)
	outText, outTruncated := stdout.String()
	errText, errTruncated := stderr.String()
	if ctx.Err() != nil {
		_ = os.RemoveAll(runDir)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ExecuteResponse{}, apiError("timeout", "OpenCLI command timed out", fmt.Sprintf("command exceeded %s", seconds))
		}
		return ExecuteResponse{}, ctx.Err()
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			_ = os.RemoveAll(runDir)
			return ExecuteResponse{}, fmt.Errorf("start opencli: %w", err)
		}
		exitCode = exitErr.ExitCode()
	}
	artifacts := []Artifact{}
	if request.Artifacts {
		artifacts, err = r.store.add(runID, runDir, r.options.MaxArtifactSize, r.options.MaxArtifactTotal)
		if err != nil {
			_ = os.RemoveAll(runDir)
			return ExecuteResponse{}, err
		}
	} else {
		_ = os.RemoveAll(runDir)
	}

	return ExecuteResponse{
		OK:              err == nil,
		RunID:           runID,
		ExitCode:        exitCode,
		Stdout:          outText,
		Stderr:          errText,
		StdoutTruncated: outTruncated,
		StderrTruncated: errTruncated,
		DurationMS:      duration.Milliseconds(),
		Artifacts:       artifacts,
	}, nil
}

func apiError(code, message, hint string) error {
	return &typedAPIError{detail: APIError{Code: code, Message: message, Hint: hint}}
}

type typedAPIError struct{ detail APIError }

func (e *typedAPIError) Error() string { return e.detail.Message }

func asAPIError(err error) (APIError, bool) {
	var typed *typedAPIError
	if errors.As(err, &typed) {
		return typed.detail, true
	}
	return APIError{}, false
}
