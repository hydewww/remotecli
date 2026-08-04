package remotecli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	clientExitGeneric            = 1
	clientExitUsage              = 2
	clientExitServiceUnavailable = 69
	clientExitAuth               = 77
)

func runRemote(args []string, stdout, stderr io.Writer) int {
	cfg, err := resolveClientConfig()
	if err != nil {
		fmt.Fprintln(stderr, "remotecli:", err)
		return clientExitUsage
	}
	request := ExecuteRequest{Args: append([]string(nil), args...), Artifacts: true}
	body, err := json.Marshal(request)
	if err != nil {
		fmt.Fprintln(stderr, "remotecli: encode request:", err)
		return clientExitGeneric
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinEndpoint(cfg.Endpoint, "/v1/execute"), strings.NewReader(string(body)))
	if err != nil {
		fmt.Fprintln(stderr, "remotecli: create request:", err)
		return clientExitUsage
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	response, err := (&http.Client{}).Do(req)
	if err != nil {
		fmt.Fprintln(stderr, "remotecli: API request failed:", err)
		return clientExitServiceUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var envelope errorResponse
		_ = json.NewDecoder(response.Body).Decode(&envelope)
		message := envelope.Error.Message
		if message == "" {
			message = response.Status
		}
		fmt.Fprintln(stderr, "remotecli:", message)
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return clientExitAuth
		case http.StatusBadRequest:
			return clientExitUsage
		default:
			return clientExitServiceUnavailable
		}
	}
	var result ExecuteResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		fmt.Fprintln(stderr, "remotecli: decode response:", err)
		return clientExitServiceUnavailable
	}
	if result.Stdout != "" {
		_, _ = io.WriteString(stdout, result.Stdout)
	}
	if result.Stderr != "" {
		_, _ = io.WriteString(stderr, result.Stderr)
	}
	if result.StdoutTruncated {
		fmt.Fprintln(stderr, "remotecli: warning: remote stdout was truncated")
	}
	if result.StderrTruncated {
		fmt.Fprintln(stderr, "remotecli: warning: remote stderr was truncated")
	}
	for _, artifact := range result.Artifacts {
		if err := downloadArtifact(ctx, cfg, result.RunID, artifact); err != nil {
			fmt.Fprintln(stderr, "remotecli: download artifact:", err)
			return clientExitGeneric
		}
		fmt.Fprintf(stderr, "remotecli: saved %s (%d bytes)\n", artifact.Path, artifact.Size)
	}
	if result.ExitCode >= 0 && result.ExitCode <= 255 {
		return result.ExitCode
	}
	return clientExitGeneric
}

func downloadArtifact(ctx context.Context, cfg Config, runID string, artifact Artifact) error {
	if runID == "" || artifact.ID == "" {
		return errors.New("response contains an invalid artifact identity")
	}
	rel, err := validateArtifactPath(artifact.Path)
	if err != nil {
		return err
	}
	target, err := safeLocalTarget(rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("refusing to overwrite existing path %s", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := validateArtifactParents(target); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	response, err := newArtifactRequest(ctx, cfg, runID, artifact.ID)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned %s", response.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".remotecli-artifact-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	hash := sha256.New()
	written, err := io.Copy(tmp, io.TeeReader(response.Body, hash))
	if err != nil {
		tmp.Close()
		return err
	}
	if artifact.Size >= 0 && written != artifact.Size {
		tmp.Close()
		return fmt.Errorf("artifact size mismatch for %s: got %d, want %d", artifact.Path, written, artifact.Size)
	}
	if artifact.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), artifact.SHA256) {
		tmp.Close()
		return fmt.Errorf("artifact checksum mismatch for %s", artifact.Path)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return err
	}
	return nil
}

func validateArtifactParents(target string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	parent := filepath.Dir(target)
	relative, err := filepath.Rel(cwd, parent)
	if err != nil {
		return err
	}
	if relative == "." {
		return nil
	}
	current := cwd
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("artifact parent is a symlink: %s", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("artifact parent is not a directory: %s", current)
			}
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		// Once a component is absent, no existing descendant can be a symlink;
		// MkdirAll will create the remaining components as directories.
		break
	}
	return nil
}

func newArtifactRequest(ctx context.Context, cfg Config, runID, artifactID string) (*http.Response, error) {
	endpoint, err := normalizeEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	requestURL := joinEndpoint(endpoint, "/v1/runs/"+url.PathEscape(runID)+"/artifacts/"+url.PathEscape(artifactID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	return (&http.Client{}).Do(req)
}

func validateArtifactPath(raw string) (string, error) {
	if raw == "" || strings.ContainsAny(raw, "\x00\\") {
		return "", errors.New("artifact path is invalid")
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "\\") || filepath.VolumeName(raw) != "" {
		return "", fmt.Errorf("refusing absolute artifact path %q", raw)
	}
	normalized := filepath.FromSlash(raw)
	clean := filepath.Clean(normalized)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing unsafe artifact path %q", raw)
	}
	return clean, nil
}

func safeLocalTarget(relative string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	target := filepath.Join(cwd, relative)
	rel, err := filepath.Rel(cwd, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact path escapes current directory")
	}
	return target, nil
}
