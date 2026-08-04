package remotecli

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func normalizeEndpoint(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" {
		return "", errors.New("endpoint is required")
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid endpoint %q: use http://host:port or https://host:port", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid endpoint scheme %q: only http and https are supported", u.Scheme)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("endpoint must not contain credentials, query parameters, or a fragment")
	}
	return value, nil
}

func loadConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Endpoint != "" {
		cfg.Endpoint, err = normalizeEndpoint(cfg.Endpoint)
		if err != nil {
			return Config{}, fmt.Errorf("config endpoint: %w", err)
		}
	}
	return cfg, nil
}

func saveConfig(cfg Config) error {
	endpoint, err := normalizeEndpoint(cfg.Endpoint)
	if err != nil {
		return err
	}
	cfg.Endpoint = endpoint
	path, err := configPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect temporary config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	// Rename is atomic on Unix. On Windows an existing destination may need to
	// be removed first because MoveFileEx semantics vary by filesystem.
	if err := os.Rename(tmpName, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("replace config: %w", err)
		}
		if retryErr := os.Rename(tmpName, path); retryErr != nil {
			return fmt.Errorf("replace config: %w", retryErr)
		}
	}
	return nil
}

func deleteConfig() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove config: %w", err)
	}
	return nil
}

func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("token file is empty")
	}
	return token, nil
}

func resolveClientConfig() (Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return Config{}, err
	}
	if value := strings.TrimSpace(os.Getenv("REMOTECLI_ENDPOINT")); value != "" {
		cfg.Endpoint = value
	}
	if value := os.Getenv("REMOTECLI_TOKEN"); value != "" {
		cfg.Token = value
	}
	cfg.Endpoint, err = normalizeEndpoint(cfg.Endpoint)
	if err != nil {
		return Config{}, fmt.Errorf("remotecli endpoint is not configured: %w", err)
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return Config{}, errors.New("remotecli token is not configured; use remotecli config --token-file or REMOTECLI_TOKEN")
	}
	return cfg, nil
}

func tokensEqual(expected, actual string) bool {
	if expected == "" || actual == "" {
		return false
	}
	a := []byte(expected)
	b := []byte(actual)
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

func readServerToken(inline, tokenFile string) (string, error) {
	envToken := os.Getenv("REMOTECLI_API_TOKEN")
	count := 0
	if strings.TrimSpace(inline) != "" {
		count++
	}
	if strings.TrimSpace(tokenFile) != "" {
		count++
	}
	if strings.TrimSpace(envToken) != "" {
		count++
	}
	if count > 1 {
		return "", errors.New("choose only one of --token, --token-file, and REMOTECLI_API_TOKEN")
	}
	var token string
	var err error
	switch {
	case strings.TrimSpace(inline) != "":
		token = inline
	case strings.TrimSpace(tokenFile) != "":
		token, err = readTokenFile(tokenFile)
	case strings.TrimSpace(envToken) != "":
		token = envToken
	default:
		return "", errors.New("server token is required; use --token-file or REMOTECLI_API_TOKEN")
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(token) == "" {
		return "", errors.New("server token is empty")
	}
	return token, nil
}

func maskTokenConfigured(token string) string {
	if token == "" {
		return "not configured"
	}
	return "configured"
}

func configJSONForDisplay(cfg Config) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]string{
		"endpoint": cfg.Endpoint,
		"token":    maskTokenConfigured(cfg.Token),
	})
	return b.String()
}
