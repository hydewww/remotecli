package remotecli

import "time"

const (
	Version                 = "0.1.0"
	DefaultPort             = 19826
	DefaultConcurrency      = 1
	DefaultCommandTimeout   = 15 * time.Minute
	DefaultRetention        = time.Hour
	DefaultMaxOutput        = 16 << 20
	DefaultMaxArtifactSize  = 512 << 20
	DefaultMaxArtifactTotal = 2 << 30
	DefaultMaxRequestBody   = 1 << 20
)

// Config is the client-side configuration stored in the user's config directory.
// The token is deliberately never rendered by config --show.
type Config struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token,omitempty"`
}

type ExecuteRequest struct {
	Args      []string `json:"args"`
	TimeoutMS int64    `json:"timeoutMs,omitempty"`
	Artifacts bool     `json:"artifacts"`
}

type ExecuteResponse struct {
	OK              bool       `json:"ok"`
	RunID           string     `json:"runId"`
	ExitCode        int        `json:"exitCode"`
	Signal          string     `json:"signal,omitempty"`
	Stdout          string     `json:"stdout"`
	Stderr          string     `json:"stderr"`
	StdoutTruncated bool       `json:"stdoutTruncated,omitempty"`
	StderrTruncated bool       `json:"stderrTruncated,omitempty"`
	DurationMS      int64      `json:"durationMs"`
	Artifacts       []Artifact `json:"artifacts"`
}

type Artifact struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	MediaType   string `json:"mediaType"`
	DownloadURL string `json:"downloadUrl"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type errorResponse struct {
	Error APIError `json:"error"`
}
