package libfetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// newAuthTestDownloader returns a Downloader pointed at a local test server,
// with fast retries so rate-limit tests do not stall.
func newAuthTestDownloader(apiURL, token string, retryCount int) *Downloader {
	return &Downloader{
		RetryCount:      retryCount,
		RetryDelay:      10 * time.Millisecond,
		ApiURL:          apiURL,
		Repo:            "owner/repo",
		ProgressTracker: nil,
		Token:           token,
	}
}

func TestLatestVersionSendsAuthHeaders(t *testing.T) {
	var gotAuth, gotUA, gotAccept, gotAPIVersion bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization") == "Bearer t0k3n"
		gotUA = r.Header.Get("User-Agent") != ""
		gotAccept = r.Header.Get("Accept") == "application/vnd.github+json"
		gotAPIVersion = r.Header.Get("X-GitHub-Api-Version") == "2022-11-28"
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","assets":[{"name":"tool.zip"}]}`))
	}))
	defer srv.Close()

	d := newAuthTestDownloader(srv.URL, "t0k3n", 1)

	if _, err := d.LatestVersion(); err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	for _, ok := range []bool{gotAuth, gotUA, gotAccept, gotAPIVersion} {
		if !ok {
			t.Errorf("missing expected header (auth=%v ua=%v accept=%v apiversion=%v)", gotAuth, gotUA, gotAccept, gotAPIVersion)
		}
	}

	t.Run("assets request", func(t *testing.T) {
		if _, err := d.GetLatestReleaseAssets(); err != nil {
			t.Fatalf("GetLatestReleaseAssets: %v", err)
		}
	})
}

func TestAssetDownloadSendsAuthHeaders(t *testing.T) {
	var gotAuth, gotUA bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization") == "Bearer t0k3n"
		gotUA = r.Header.Get("User-Agent") != ""
		w.Header().Set("Content-Length", "2")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	dest := t.TempDir()
	d := newAuthTestDownloader(srv.URL, "t0k3n", 1)
	d.assetBaseURL = srv.URL

	if err := d.get(context.Background(), srv.URL+"/owner/repo/releases/download/v1.0.0/tool.bin", dest); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !gotAuth || !gotUA {
		t.Errorf("asset download missing headers (auth=%v ua=%v)", gotAuth, gotUA)
	}
	if _, err := os.Stat(filepath.Join(dest, "tool.bin")); err != nil {
		t.Errorf("expected dest/tool.bin: %v", err)
	}
}

func TestNoTokenNoAuthorizationHeader(t *testing.T) {
	sawAuth := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			sawAuth = true
		}
		if r.URL.Path == "/api" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","assets":[]}`))
			return
		}
		w.Header().Set("Content-Length", "2")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	d := newAuthTestDownloader(srv.URL+"/api", "", 1)
	if _, err := d.LatestVersion(); err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if err := d.get(context.Background(), srv.URL+"/owner/repo/releases/download/v1.0.0/tool.bin", t.TempDir()); err != nil {
		t.Fatalf("get: %v", err)
	}
	if sawAuth {
		t.Error("Authorization header sent without a configured token")
	}
}

func TestLatestVersionRetriesAfterRateLimit(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			// Primary rate limit exhausted: honor Retry-After.
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limit exceeded", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer srv.Close()

	d := newAuthTestDownloader(srv.URL, "t0k3n", 2)
	start := time.Now()
	v, err := d.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if v != "v1.0.0" {
		t.Errorf("version = %q, want v1.0.0", v)
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("retry did not honor Retry-After: slept only %v", elapsed)
	}
}

func TestRetryWait(t *testing.T) {
	d := &Downloader{RetryDelay: 5 * time.Second}

	cases := []struct {
		name string
		err  *statusError
		want time.Duration
	}{
		{
			name: "retry-after respected",
			err:  &statusError{StatusCode: 403, RetryAfter: 30 * time.Second, Remaining: 0},
			want: 30 * time.Second,
		},
		{
			name: "rate reset respected when larger",
			err:  &statusError{StatusCode: 403, RateReset: time.Now().Add(45 * time.Second), Remaining: 0},
			want: 45 * time.Second,
		},
		{
			name: "capped at maxRateLimitWait",
			err:  &statusError{StatusCode: 403, RetryAfter: time.Hour, Remaining: 0},
			want: maxRateLimitWait,
		},
		{
			name: "rate limited but no usable headers falls back to RetryDelay",
			err:  &statusError{StatusCode: 429, Remaining: -1},
			want: 5 * time.Second,
		},
		{
			name: "non-rate-limit status uses RetryDelay",
			err:  &statusError{StatusCode: 500},
			want: 5 * time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := d.retryWait(tc.err)
			// Compare with tolerance: time.Until drifts by microseconds.
			if diff := got - tc.want; diff < -50*time.Millisecond || diff > 50*time.Millisecond {
				t.Errorf("retryWait = %v, want ~%v", got, tc.want)
			}
		})
	}

	if got := d.retryWait(errors.New("network error")); got != 5*time.Second {
		t.Errorf("retryWait(non-status) = %v, want 5s", got)
	}
}

func TestLatestVersionFailsFastOnClientError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Error(w, "client error", status)
			}))
			defer srv.Close()

			d := newAuthTestDownloader(srv.URL, "bad-token", 3)
			_, err := d.LatestVersion()
			if err == nil {
				t.Fatal("expected an error")
			}
			var se *statusError
			if !errors.As(err, &se) {
				t.Fatalf("error is not a *statusError: %v", err)
			}
			if se.StatusCode != status {
				t.Errorf("status = %d, want %d", se.StatusCode, status)
			}
			if n := requests.Load(); n != 1 {
				t.Errorf("requests = %d, want 1 (client errors must not be retried)", n)
			}
		})
	}
}

func TestLatestVersionRetriesOnServerError(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v2.0.0"}`))
	}))
	defer srv.Close()

	d := newAuthTestDownloader(srv.URL, "", 3)
	v, err := d.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if v != "v2.0.0" {
		t.Errorf("version = %q, want v2.0.0", v)
	}
	if n := requests.Load(); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
}
