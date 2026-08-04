package remotecli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("REMOTECLI_TEST_HELPER") == "1" {
		helperProcess()
		return
	}
	os.Exit(m.Run())
}

func helperProcess() {
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--sleep-ms=") {
			var milliseconds int
			_, _ = fmt.Sscanf(strings.TrimPrefix(arg, "--sleep-ms="), "%d", &milliseconds)
			if milliseconds > 0 {
				time.Sleep(time.Duration(milliseconds) * time.Millisecond)
			}
		}
		if arg == "--make-artifact" {
			_ = os.MkdirAll(filepath.Join("nested", "dir"), 0o755)
			_ = os.WriteFile(filepath.Join("nested", "dir", "result.txt"), []byte("artifact-content"), 0o644)
		}
		if arg == "--exit-7" {
			_, _ = io.WriteString(os.Stdout, "child stdout\n")
			_, _ = io.WriteString(os.Stderr, "child stderr\n")
			os.Exit(7)
		}
	}
	_, _ = io.WriteString(os.Stdout, "child stdout\n")
	_, _ = io.WriteString(os.Stderr, "child stderr\n")
	os.Exit(0)
}

func helperPath(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNormalizeEndpoint(t *testing.T) {
	got, err := normalizeEndpoint(" https://example.test:19826/// ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.test:19826" {
		t.Fatalf("got %q", got)
	}
	for _, raw := range []string{"", "example.test:19826", "ftp://example.test", "http://user:pass@example.test", "http://example.test?a=1"} {
		if _, err := normalizeEndpoint(raw); err == nil {
			t.Errorf("expected %q to be rejected", raw)
		}
	}
}

func TestConfigRoundTripAndPermissions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	want := Config{Endpoint: "http://127.0.0.1:19826", Token: "secret"}
	if err := saveConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("config mode %o, want 600", mode)
		}
	}
	if err := deleteConfig(); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactPathValidation(t *testing.T) {
	for _, raw := range []string{"../escape", "/tmp/escape", `..\\escape`, ""} {
		if _, err := validateArtifactPath(raw); err == nil {
			t.Errorf("expected %q to be rejected", raw)
		}
	}
	got, err := validateArtifactPath("nested/result.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join("nested", "result.txt") {
		t.Fatalf("got %q", got)
	}
}

func TestTokensEqual(t *testing.T) {
	if !tokensEqual("secret", "secret") {
		t.Fatal("equal tokens did not match")
	}
	if tokensEqual("secret", "other") {
		t.Fatal("different tokens matched")
	}
	if tokensEqual("", "") {
		t.Fatal("empty tokens matched")
	}
}

func TestRunnerCapturesOutputExitCodeAndArtifacts(t *testing.T) {
	t.Setenv("REMOTECLI_TEST_HELPER", "1")
	runner, err := NewRunner(RunnerOptions{
		OpenCLIPath:      helperPath(t),
		RunRoot:          t.TempDir(),
		CommandTimeout:   10 * time.Second,
		MaxArtifactSize:  1024,
		MaxArtifactTotal: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Execute(context.Background(), ExecuteRequest{Args: []string{"--make-artifact"}, Artifacts: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.ExitCode != 0 {
		t.Fatalf("unexpected result %#v", result)
	}
	if result.Stdout != "child stdout\n" || result.Stderr != "child stderr\n" {
		t.Fatalf("unexpected output %#v", result)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Path != "nested/dir/result.txt" {
		t.Fatalf("unexpected artifacts %#v", result.Artifacts)
	}
	stored, ok := runner.store.get(result.RunID, result.Artifacts[0].ID)
	if !ok {
		t.Fatal("stored artifact not found")
	}
	data, err := os.ReadFile(stored.AbsolutePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "artifact-content" {
		t.Fatalf("artifact content %q", data)
	}
}

func TestRunnerResolvesRelativeOpenCLIPathBeforeChangingDirectory(t *testing.T) {
	t.Setenv("REMOTECLI_TEST_HELPER", "1")
	path := helperPath(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, path)
	if err != nil {
		if runtime.GOOS != "windows" {
			t.Fatal(err)
		}
		originalCWD := cwd
		if err := os.Chdir(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(originalCWD) })
		cwd, err = os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		relative, err = filepath.Rel(cwd, path)
		if err != nil {
			t.Fatal(err)
		}
	}
	configuredPath := "." + string(filepath.Separator) + relative
	runner, err := NewRunner(RunnerOptions{OpenCLIPath: configuredPath, RunRoot: t.TempDir(), CommandTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Execute(context.Background(), ExecuteRequest{Args: []string{"--version"}, Artifacts: false})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestRunnerPreservesNonZeroOpenCLIExit(t *testing.T) {
	t.Setenv("REMOTECLI_TEST_HELPER", "1")
	runner, err := NewRunner(RunnerOptions{OpenCLIPath: helperPath(t), RunRoot: t.TempDir(), CommandTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Execute(context.Background(), ExecuteRequest{Args: []string{"--exit-7"}, Artifacts: false})
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.ExitCode != 7 {
		t.Fatalf("unexpected result %#v", result)
	}
	if !strings.Contains(result.Stdout, "child stdout") || !strings.Contains(result.Stderr, "child stderr") {
		t.Fatalf("output missing %#v", result)
	}
}

func TestRunnerAppliesCommandTimeout(t *testing.T) {
	t.Setenv("REMOTECLI_TEST_HELPER", "1")
	runner, err := NewRunner(RunnerOptions{OpenCLIPath: helperPath(t), RunRoot: t.TempDir(), CommandTimeout: 25 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Execute(context.Background(), ExecuteRequest{Args: []string{"--sleep-ms=250"}, Artifacts: false})
	if err == nil {
		t.Fatal("expected timeout")
	}
	detail, ok := asAPIError(err)
	if !ok || detail.Code != "timeout" {
		t.Fatalf("unexpected error %#v", err)
	}
}

func TestServerAuthExecuteAndArtifactDownload(t *testing.T) {
	t.Setenv("REMOTECLI_TEST_HELPER", "1")
	server, err := NewServer(ServerOptions{
		Token:            "secret",
		OpenCLIPath:      helperPath(t),
		RunRoot:          t.TempDir(),
		CommandTimeout:   10 * time.Second,
		MaxArtifactSize:  1024,
		MaxArtifactTotal: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	unauthorized, err := http.Post(ts.URL+"/v1/execute", "application/json", strings.NewReader(`{"args":[],"artifacts":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", unauthorized.StatusCode)
	}
	_ = unauthorized.Body.Close()

	body, _ := json.Marshal(ExecuteRequest{Args: []string{"--make-artifact"}, Artifacts: true})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
	var result ExecuteResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifacts %#v", result.Artifacts)
	}

	artifactReq, _ := http.NewRequest(http.MethodGet, ts.URL+result.Artifacts[0].DownloadURL, nil)
	artifactReq.Header.Set("Authorization", "Bearer secret")
	artifactResponse, err := (&http.Client{}).Do(artifactReq)
	if err != nil {
		t.Fatal(err)
	}
	defer artifactResponse.Body.Close()
	if artifactResponse.StatusCode != http.StatusOK {
		t.Fatalf("artifact status %d", artifactResponse.StatusCode)
	}
	data, err := io.ReadAll(artifactResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "artifact-content" {
		t.Fatalf("artifact content %q", data)
	}
}

func TestRunRemoteDownloadsArtifacts(t *testing.T) {
	t.Setenv("REMOTECLI_TEST_HELPER", "1")
	server, err := NewServer(ServerOptions{Token: "secret", OpenCLIPath: helperPath(t), RunRoot: t.TempDir(), CommandTimeout: 10 * time.Second, MaxArtifactSize: 1024, MaxArtifactTotal: 2048})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	t.Setenv("REMOTECLI_ENDPOINT", ts.URL)
	t.Setenv("REMOTECLI_TOKEN", "secret")
	work := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	var stdout, stderr bytes.Buffer
	code := runRemote([]string{"--make-artifact"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	if stdout.String() != "child stdout\n" {
		t.Fatalf("stdout %q", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(work, "nested", "dir", "result.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "artifact-content" {
		t.Fatalf("artifact content %q", data)
	}
}
