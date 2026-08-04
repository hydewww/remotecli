package remotecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type ServerOptions struct {
	Bind             string
	Port             int
	Token            string
	OpenCLIPath      string
	RunRoot          string
	Concurrency      int
	CommandTimeout   time.Duration
	Retention        time.Duration
	MaxOutput        int
	MaxArtifactSize  int64
	MaxArtifactTotal int64
}

type Server struct {
	options ServerOptions
	runner  *Runner
	mux     *http.ServeMux
	server  *http.Server
	stop    chan struct{}
}

func NewServer(options ServerOptions) (*Server, error) {
	runner, err := NewRunner(RunnerOptions{
		OpenCLIPath:      options.OpenCLIPath,
		RunRoot:          options.RunRoot,
		Concurrency:      options.Concurrency,
		CommandTimeout:   options.CommandTimeout,
		MaxOutput:        options.MaxOutput,
		MaxArtifactSize:  options.MaxArtifactSize,
		MaxArtifactTotal: options.MaxArtifactTotal,
		Retention:        options.Retention,
	})
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	s := &Server{options: options, runner: runner, mux: mux, stop: make(chan struct{})}
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/execute", s.handleExecute)
	mux.HandleFunc("/v1/runs/", s.handleArtifact)
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) ListenAndServe(ctx context.Context) error {
	bind := s.options.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	port := s.options.Port
	if port == 0 {
		port = DefaultPort
	}
	address := net.JoinHostPort(bind, strconv.Itoa(port))
	s.server = &http.Server{Addr: address, Handler: s.mux, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 64 << 10}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	go s.cleanupLoop()
	go func() {
		<-ctx.Done()
		s.runner.CancelAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
		close(s.stop)
	}()
	err = s.server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.runner.store.cleanup()
		case <-s.stop:
			return
		}
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, APIError{Code: "method_not_allowed", Message: "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"service": "remotecli",
		"version": Version,
	})
}

func (s *Server) authorized(r *http.Request) bool {
	value := r.Header.Get("Authorization")
	parts := strings.SplitN(value, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	return tokensEqual(s.options.Token, strings.TrimSpace(parts[1]))
}

func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, APIError{Code: "method_not_allowed", Message: "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeAPIError(w, http.StatusUnauthorized, APIError{Code: "unauthorized", Message: "valid Bearer token required"})
		return
	}
	if r.Body == nil {
		writeAPIError(w, http.StatusBadRequest, APIError{Code: "invalid_json", Message: "request body is required"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, DefaultMaxRequestBody)
	defer r.Body.Close()
	var request ExecuteRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		writeAPIError(w, http.StatusBadRequest, APIError{Code: "invalid_json", Message: "request body must be valid JSON"})
		return
	}
	if len(request.Args) == 0 {
		// Empty argv is intentional: it asks OpenCLI for its root help.
	}
	if request.TimeoutMS < 0 {
		writeAPIError(w, http.StatusBadRequest, APIError{Code: "invalid_timeout", Message: "timeoutMs must be non-negative"})
		return
	}
	response, err := s.runner.Execute(r.Context(), request)
	if err != nil {
		if detail, ok := asAPIError(err); ok {
			status := http.StatusBadRequest
			switch detail.Code {
			case "timeout":
				status = http.StatusGatewayTimeout
			case "invalid_args":
				status = http.StatusBadRequest
			}
			writeAPIError(w, status, detail)
			return
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		writeAPIError(w, http.StatusServiceUnavailable, APIError{Code: "execution_failed", Message: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, APIError{Code: "method_not_allowed", Message: "method not allowed"})
		return
	}
	if !s.authorized(r) {
		writeAPIError(w, http.StatusUnauthorized, APIError{Code: "unauthorized", Message: "valid Bearer token required"})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/runs/"), "/")
	if len(parts) != 3 || parts[1] != "artifacts" || parts[0] == "" || parts[2] == "" {
		writeAPIError(w, http.StatusNotFound, APIError{Code: "artifact_not_found", Message: "artifact not found"})
		return
	}
	runID, err := url.PathUnescape(parts[0])
	if err != nil {
		writeAPIError(w, http.StatusNotFound, APIError{Code: "artifact_not_found", Message: "artifact not found"})
		return
	}
	artifactID, err := url.PathUnescape(parts[2])
	if err != nil {
		writeAPIError(w, http.StatusNotFound, APIError{Code: "artifact_not_found", Message: "artifact not found"})
		return
	}
	stored, ok := s.runner.store.get(runID, artifactID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, APIError{Code: "artifact_not_found", Message: "artifact not found or expired"})
		return
	}
	info, err := os.Lstat(stored.AbsolutePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		writeAPIError(w, http.StatusGone, APIError{Code: "artifact_unavailable", Message: "artifact is no longer available"})
		return
	}
	file, err := os.Open(stored.AbsolutePath)
	if err != nil {
		writeAPIError(w, http.StatusGone, APIError{Code: "artifact_unavailable", Message: "artifact is no longer available"})
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", stored.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(stored.Size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+artifactName(stored.Path)+`"`)
	_, _ = io.Copy(w, file)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, detail APIError) {
	writeJSON(w, status, errorResponse{Error: detail})
}

func joinEndpoint(endpoint, suffix string) string {
	return strings.TrimRight(endpoint, "/") + "/" + strings.TrimLeft(path.Clean(suffix), "/")
}
