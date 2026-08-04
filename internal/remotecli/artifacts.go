package remotecli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type storedArtifact struct {
	Artifact
	AbsolutePath string
}

type storedRun struct {
	ID        string
	Root      string
	ExpiresAt time.Time
	Artifacts map[string]storedArtifact
}

type runStore struct {
	mu        sync.RWMutex
	runs      map[string]*storedRun
	retention time.Duration
}

func newRunStore(retention time.Duration) *runStore {
	return &runStore{runs: make(map[string]*storedRun), retention: retention}
}

func (s *runStore) add(runID, root string, maxFile, maxTotal int64) ([]Artifact, error) {
	entries := make([]string, 0)
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		// Symlinks are never returned. This prevents a command from making the
		// artifact endpoint expose an arbitrary file outside its run directory.
		if entry.Type()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !safeRelativePath(rel) {
			return fmt.Errorf("unsafe artifact path %q", rel)
		}
		if info.Size() > maxFile {
			return fmt.Errorf("artifact %q is %d bytes, above the %d-byte limit", rel, info.Size(), maxFile)
		}
		if total+info.Size() > maxTotal {
			return fmt.Errorf("artifacts exceed the %d-byte total limit", maxTotal)
		}
		total += info.Size()
		entries = append(entries, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(entries)
	manifest := make([]Artifact, 0, len(entries))
	stored := make(map[string]storedArtifact, len(entries))
	for _, rel := range entries {
		absolute := filepath.Join(root, rel)
		artifact, err := describeArtifact(absolute, filepath.ToSlash(rel))
		if err != nil {
			return nil, err
		}
		artifact.ID, err = newID()
		if err != nil {
			return nil, err
		}
		artifact.DownloadURL = "/v1/runs/" + runID + "/artifacts/" + artifact.ID
		manifest = append(manifest, artifact)
		stored[artifact.ID] = storedArtifact{Artifact: artifact, AbsolutePath: absolute}
	}
	s.mu.Lock()
	s.runs[runID] = &storedRun{
		ID:        runID,
		Root:      root,
		ExpiresAt: time.Now().Add(s.retention),
		Artifacts: stored,
	}
	s.mu.Unlock()
	return manifest, nil
}

func describeArtifact(path, relative string) (Artifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Artifact{}, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return Artifact{}, err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return Artifact{}, err
	}
	preview := make([]byte, 512)
	n, _ := file.Read(preview)
	mediaType := http.DetectContentType(preview[:n])
	return Artifact{
		Path:      relative,
		Size:      info.Size(),
		SHA256:    hex.EncodeToString(hash.Sum(nil)),
		MediaType: mediaType,
	}, nil
}

func safeRelativePath(raw string) bool {
	if raw == "" || filepath.IsAbs(raw) {
		return false
	}
	clean := filepath.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func (s *runStore) get(runID, artifactID string) (storedArtifact, bool) {
	s.mu.RLock()
	run, ok := s.runs[runID]
	if ok && time.Now().After(run.ExpiresAt) {
		ok = false
	}
	var artifact storedArtifact
	if ok {
		artifact, ok = run.Artifacts[artifactID]
	}
	s.mu.RUnlock()
	if !ok {
		return storedArtifact{}, false
	}
	return artifact, true
}

func (s *runStore) cleanup() {
	now := time.Now()
	var expired []*storedRun
	s.mu.Lock()
	for id, run := range s.runs {
		if now.After(run.ExpiresAt) {
			expired = append(expired, run)
			delete(s.runs, id)
		}
	}
	s.mu.Unlock()
	for _, run := range expired {
		_ = os.RemoveAll(run.Root)
	}
}

func (s *runStore) closeRun(runID string) {
	s.mu.Lock()
	run, ok := s.runs[runID]
	if ok {
		delete(s.runs, runID)
	}
	s.mu.Unlock()
	if ok {
		_ = os.RemoveAll(run.Root)
	}
}

func artifactName(value string) string {
	name := filepath.Base(filepath.FromSlash(value))
	name = strings.ReplaceAll(name, `"`, "'")
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "artifact"
	}
	return name
}
