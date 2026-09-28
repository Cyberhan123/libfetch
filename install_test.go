package libfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// assetRecorder records the asset download paths the fake server served.
type assetRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (r *assetRecorder) add(p string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, p)
}

func (r *assetRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

// newFakeGitHub starts a local server that mimics the two GitHub endpoints
// libfetch uses: the latest-release API and release asset downloads. The
// served release has tag v1.0.0 with a single tool.zip asset whose archive
// extracts to bin/tool.
func newFakeGitHub(t *testing.T) (*httptest.Server, *atomic.Int32, *assetRecorder) {
	t.Helper()
	zipData := buildZip(t, []zipEntry{
		{name: "tool-1.0.0/bin/", isDir: true},
		{name: "tool-1.0.0/bin/tool", content: "tool-v1", mode: 0755},
	})

	var apiCalls atomic.Int32
	assets := &assetRecorder{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","assets":[{"name":"tool.zip"}]}`))
	})
	mux.HandleFunc("/o/r/releases/download/", func(w http.ResponseWriter, r *http.Request) {
		assets.add(r.URL.Path)
		w.Header().Set("Content-Length", strconv.Itoa(len(zipData)))
		_, _ = w.Write(zipData)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv, &apiCalls, assets
}

// newFakeInstall wires an Install at dir against the fake GitHub server.
func newFakeInstall(srv *httptest.Server, dir string, retryCount int) *Install {
	d := &Downloader{
		RetryCount: retryCount,
		RetryDelay: 10 * time.Millisecond,
		ApiURL:     srv.URL + "/api/repos/o/r/releases/latest",
		Repo:       "o/r",
	}
	d.assetBaseURL = srv.URL
	i := NewInstall("o/r", dir)
	i.Downloader = d
	return i
}

func TestConcurrentInstallAssetSameDir(t *testing.T) {
	srv, _, _ := newFakeGitHub(t)
	dest := t.TempDir()

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[w] = newFakeInstall(srv, dest, 1).InstallAsset("tool.zip", "v1.0.0", true)
		}()
	}
	wg.Wait()

	for w, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", w, err)
		}
	}

	got, err := os.ReadFile(filepath.Join(dest, "bin", "tool"))
	if err != nil {
		t.Fatalf("expected bin/tool to be extracted: %v", err)
	}
	if string(got) != "tool-v1" {
		t.Errorf("bin/tool content = %q, want %q", got, "tool-v1")
	}

	var vi VersionInfo
	data, err := os.ReadFile(filepath.Join(dest, "version.json"))
	if err != nil {
		t.Fatalf("read version.json: %v", err)
	}
	if err := json.Unmarshal(data, &vi); err != nil {
		t.Fatalf("version.json is not valid JSON: %v", err)
	}
	if vi.TagName != "v1.0.0" || vi.Repo != "o/r" {
		t.Errorf("version.json = %+v, want {v1.0.0 o/r}", vi)
	}

	assertNoTempResidue(t, dest)
}

func TestUpgradeDownloadsNewAssetName(t *testing.T) {
	srv, apiCalls, assets := newFakeGitHub(t)
	dest := t.TempDir()

	// Pre-seed an old installation (v0.9.0 with a leftover file).
	if err := os.WriteFile(filepath.Join(dest, "version.json"), []byte(`{"tag_name":"v0.9.0","repo":"o/r"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "old.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := newFakeInstall(srv, dest, 1).InstallAsset("tool.zip", "", true); err != nil {
		t.Fatalf("InstallAsset: %v", err)
	}

	// Regression for the old bug: the upgrade must download the NEW release
	// asset (v1.0.0), not the currently installed tag (v0.9.0).
	served := assets.snapshot()
	if len(served) != 1 {
		t.Fatalf("asset requests = %v, want exactly one", served)
	}
	if !strings.HasSuffix(served[0], "/v1.0.0/tool.zip") {
		t.Errorf("asset request = %q, want the v1.0.0 asset", served[0])
	}

	var vi VersionInfo
	data, err := os.ReadFile(filepath.Join(dest, "version.json"))
	if err != nil {
		t.Fatalf("read version.json: %v", err)
	}
	if err := json.Unmarshal(data, &vi); err != nil {
		t.Fatalf("version.json is not valid JSON: %v", err)
	}
	if vi.TagName != "v1.0.0" {
		t.Errorf("version.json tag = %q, want v1.0.0", vi.TagName)
	}
	if _, err := os.Stat(filepath.Join(dest, "old.txt")); err == nil {
		t.Error("old.txt survived the upgrade; the installation directory should be replaced")
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "tool")); err != nil {
		t.Errorf("expected bin/tool after upgrade: %v", err)
	}

	// The whole upgrade must need a single API call (the latest check).
	if n := apiCalls.Load(); n != 1 {
		t.Errorf("API calls = %d, want 1", n)
	}

	// No staging directories left next to the install dir.
	parent, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range parent {
		if strings.HasPrefix(e.Name(), ".libfetch-upgrade-") {
			t.Errorf("staging dir left behind: %s", e.Name())
		}
	}
}

func TestUpgradeFailureKeepsPreviousInstall(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/repos/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","assets":[{"name":"tool.zip"}]}`))
	})
	mux.HandleFunc("/o/r/releases/download/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "version.json"), []byte(`{"tag_name":"v0.9.0","repo":"o/r"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "old.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := newFakeInstall(srv, dest, 1).InstallAsset("tool.zip", "", true); err == nil {
		t.Fatal("expected the upgrade to fail when the asset download fails")
	}

	// A failed download must leave the previous installation untouched.
	data, err := os.ReadFile(filepath.Join(dest, "version.json"))
	if err != nil {
		t.Fatalf("version.json gone after failed upgrade: %v", err)
	}
	if !strings.Contains(string(data), "v0.9.0") {
		t.Errorf("version.json = %s, want the old v0.9.0 entry", data)
	}
	if _, err := os.Stat(filepath.Join(dest, "old.txt")); err != nil {
		t.Errorf("old.txt lost after failed upgrade: %v", err)
	}
}

func TestConcurrentDownloadAssetSameDest(t *testing.T) {
	srv, _, _ := newFakeGitHub(t)
	dest := t.TempDir()

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := &Downloader{
				RetryCount: 1,
				RetryDelay: 10 * time.Millisecond,
				ApiURL:     srv.URL + "/api/repos/o/r/releases/latest",
				Repo:       "o/r",
			}
			d.assetBaseURL = srv.URL
			errs[w] = d.DownloadAssetWithContext(context.Background(), "tool.zip", "v1.0.0", dest)
		}()
	}
	wg.Wait()

	for w, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", w, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "tool")); err != nil {
		t.Errorf("expected bin/tool to be extracted: %v", err)
	}
	assertNoTempResidue(t, dest)
}

func TestCreateVersionFileConcurrentAtomic(t *testing.T) {
	dest := t.TempDir()
	i := NewInstall("o/r", dest)

	versions := []string{"v1.0.0", "v1.1.0", "v1.2.0", "v1.3.0", "v1.4.0", "v1.5.0", "v1.6.0", "v1.7.0"}
	var wg sync.WaitGroup
	errs := make([]error, len(versions))
	for w, v := range versions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[w] = i.CreateVersionFile(v)
		}()
	}
	wg.Wait()

	for w, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", w, err)
		}
	}

	// The final file must be complete valid JSON for exactly one writer —
	// never a torn interleaving of two concurrent writes.
	data, err := os.ReadFile(filepath.Join(dest, "version.json"))
	if err != nil {
		t.Fatalf("read version.json: %v", err)
	}
	var vi VersionInfo
	if err := json.Unmarshal(data, &vi); err != nil {
		t.Fatalf("version.json is not valid JSON: %v", err)
	}
	found := false
	for _, v := range versions {
		if vi.TagName == v {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("version.json tag = %q, want one of %v", vi.TagName, versions)
	}
	assertNoTempResidue(t, dest)
}

func TestDownloadLatestAssetSingleAPIRequest(t *testing.T) {
	srv, apiCalls, _ := newFakeGitHub(t)
	dest := t.TempDir()

	i := newFakeInstall(srv, dest, 1)
	if err := i.Downloader.DownloadLatestAsset("tool", dest); err != nil {
		t.Fatalf("DownloadLatestAsset: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "bin", "tool")); err != nil {
		t.Errorf("expected bin/tool to be extracted: %v", err)
	}
	// Regression: tag and asset list must come from a single API response.
	if n := apiCalls.Load(); n != 1 {
		t.Errorf("API calls = %d, want 1", n)
	}
}
