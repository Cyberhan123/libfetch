package libfetch

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Downloader struct {
	// RetryCount is how many times the package will retry to obtain the latest version.
	RetryCount int
	// RetryDelay is the delay between retries when obtaining the latest version.
	RetryDelay time.Duration
	// ApiURL is the GitHub API URL for fetching the latest release.
	ApiURL string
	// Repo is the GitHub repository in format "owner/repo".
	Repo string
	// Proxy is the HTTP proxy to use for downloads.
	Proxy string
	// ProgressTracker is the progress tracker to use for downloads.
	ProgressTracker ProgressTracker
	// Token is an optional GitHub personal access token. When set it is sent
	// as "Authorization: Bearer <token>" on API requests and asset downloads,
	// which is required for private repositories and raises the API rate
	// limit.
	Token string

	// assetBaseURL overrides the GitHub download host (empty means
	// https://github.com). Unexported on purpose: it exists so tests can
	// redirect asset downloads to a local server.
	assetBaseURL string
}

const (
	// userAgent identifies libfetch to GitHub, which requires a User-Agent on
	// API requests.
	userAgent = "libfetch"
	// maxRateLimitWait caps how long a retry sleeps on rate-limit headers, so
	// an far-future X-RateLimit-Reset cannot stall the caller indefinitely.
	maxRateLimitWait = 2 * time.Minute
)

func NewDownloader(repo string) *Downloader {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	return &Downloader{
		RetryCount:      3,
		RetryDelay:      3 * time.Second,
		ApiURL:          apiURL,
		Repo:            repo,
		Proxy:           "",
		ProgressTracker: DefaultProgressTracker(),
	}
}

func NewDownloaderWithConfig(repo string, retryCount int, retryDelay time.Duration, proxy string, progressTracker ProgressTracker) *Downloader {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	return &Downloader{
		RetryCount:      retryCount,
		RetryDelay:      retryDelay,
		ApiURL:          apiURL,
		Repo:            repo,
		Proxy:           proxy,
		ProgressTracker: progressTracker,
	}
}

// LatestVersion fetches the latest release tag from the GitHub API for the specified repository.
func (f *Downloader) LatestVersion() (string, error) {
	rel, err := f.fetchLatestRelease()
	if err != nil {
		return "", err
	}
	return rel.TagName, nil
}

// setCommonHeaders sets the headers shared by API requests and asset
// downloads. The Authorization header is only sent when a token is
// configured; Go's http.Client strips it on cross-host redirects (e.g. to
// the objects.githubusercontent.com CDN), which is the desired behavior.
func (f *Downloader) setCommonHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	if f.Token != "" {
		req.Header.Set("Authorization", "Bearer "+f.Token)
	}
}

// setAPIHeaders sets the headers required by the GitHub REST API on req.
func (f *Downloader) setAPIHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	f.setCommonHeaders(req)
}

// statusError is a non-200 response from the GitHub API, carrying the
// rate-limit hints needed to schedule retries.
type statusError struct {
	StatusCode int
	Body       string
	// RetryAfter is parsed from the Retry-After header; <=0 if absent.
	RetryAfter time.Duration
	// RateReset is parsed from X-RateLimit-Reset; zero if absent.
	RateReset time.Time
	// Remaining is parsed from X-RateLimit-Remaining; -1 if absent.
	Remaining int
}

func (e *statusError) Error() string {
	return fmt.Sprintf("received status code %d from GitHub API: %s", e.StatusCode, e.Body)
}

// rateLimited reports whether GitHub asked us to slow down.
func (e *statusError) rateLimited() bool {
	if e.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return e.StatusCode == http.StatusForbidden &&
		(e.RetryAfter > 0 || e.Remaining == 0 ||
			(!e.RateReset.IsZero() && time.Until(e.RateReset) > 0))
}

// retryable reports whether repeating the request could succeed: rate
// limits clear after waiting and 5xx may be transient, but other client
// errors (bad token 401, missing repo 404, ...) are permanent.
func (e *statusError) retryable() bool {
	return e.rateLimited() || e.StatusCode >= 500
}

// rateLimitFrom extracts the rate-limit hints from a response header set.
func rateLimitFrom(h http.Header) (retryAfter time.Duration, reset time.Time, remaining int) {
	remaining = -1
	if v := h.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			remaining = n
		}
	}
	if v := h.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			retryAfter = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			retryAfter = time.Until(t)
		}
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
			reset = time.Unix(secs, 0)
		}
	}
	return retryAfter, reset, remaining
}

// retryWait returns how long to sleep before the next attempt: rate-limited
// responses wait per their headers (capped at maxRateLimitWait), everything
// else waits the configured RetryDelay.
func (f *Downloader) retryWait(err error) time.Duration {
	var se *statusError
	if !errors.As(err, &se) || !se.rateLimited() {
		return f.RetryDelay
	}
	wait := se.RetryAfter
	if until := time.Until(se.RateReset); until > wait {
		wait = until
	}
	if wait <= 0 {
		return f.RetryDelay
	}
	return min(wait, maxRateLimitWait)
}

// latestRelease is the parsed subset of the latest-release payload that
// libfetch needs: the release tag and its asset names.
type latestRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

// fetchLatestRelease fetches the latest release (tag + asset names) with
// rate-limit-aware retry: permanent errors fail fast, rate limits wait per
// the response headers (capped), transient errors use RetryDelay.
func (f *Downloader) fetchLatestRelease() (*latestRelease, error) {
	var lastErr error
	for attempt := range f.RetryCount {
		rel, err := f.fetchLatestReleaseOnce()
		if err == nil {
			return rel, nil
		}
		lastErr = err

		var se *statusError
		if errors.As(err, &se) && !se.retryable() {
			return nil, err
		}
		if attempt < f.RetryCount-1 {
			time.Sleep(f.retryWait(err))
		}
	}

	return nil, fmt.Errorf("unable to fetch latest release after %d attempts: %w", f.RetryCount, lastErr)
}

func (f *Downloader) fetchLatestReleaseOnce() (*latestRelease, error) {
	req, err := http.NewRequest("GET", f.ApiURL, nil)
	if err != nil {
		return nil, err
	}

	// Set required headers for GitHub API (plus auth when configured)
	f.setAPIHeaders(req)

	// Create HTTP client with proxy support
	client := f.createHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		retryAfter, reset, remaining := rateLimitFrom(resp.Header)
		return nil, &statusError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
			RetryAfter: retryAfter,
			RateReset:  reset,
			Remaining:  remaining,
		}
	}

	var result latestRelease
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return &result, nil
}

// proxyTransport builds the transport shared by all HTTP clients of this
// downloader. The proxy is only attached when one is explicitly configured.
func (f *Downloader) proxyTransport() *http.Transport {
	transport := &http.Transport{}

	// Set proxy if configured
	if f.Proxy != "" {
		proxyURL, err := url.Parse(f.Proxy)
		if err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}

	return transport
}

// createHTTPClient creates an HTTP client for small GitHub API requests,
// with a 30 second whole-request timeout.
func (f *Downloader) createHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: f.proxyTransport(),
	}
}

// createDownloadClient creates an HTTP client for asset downloads. It has no
// whole-request timeout so that large downloads are not cut off; cancellation
// is driven by the request context instead.
func (f *Downloader) createDownloadClient() *http.Client {
	return &http.Client{
		Transport: f.proxyTransport(),
	}
}

// githubBase returns the base URL for release asset downloads.
func (f *Downloader) githubBase() string {
	if f.assetBaseURL != "" {
		return f.assetBaseURL
	}
	return "https://github.com"
}

// GetReleaseAssetURL returns the download URL for a specific asset in the latest release.
func (f *Downloader) GetReleaseAssetURL(assetName string) (string, error) {
	// First get the latest version
	version, err := f.LatestVersion()
	if err != nil {
		return "", err
	}

	// Construct the download URL
	baseURL := fmt.Sprintf("%s/%s/releases/download/%s", f.githubBase(), f.Repo, version)
	return fmt.Sprintf("%s/%s", baseURL, assetName), nil
}

// GetReleaseAssetURLByVersion returns the download URL for a specific asset in a specific release version.
func (f *Downloader) GetReleaseAssetURLByVersion(assetName, version string) string {
	baseURL := fmt.Sprintf("%s/%s/releases/download/%s", f.githubBase(), f.Repo, version)
	return fmt.Sprintf("%s/%s", baseURL, assetName)
}

// DownloadAsset downloads a specific asset from the latest release of the repository.
// assetName is the name of the asset to download.
// dest is the destination directory for the downloaded asset.
//
// It is safe for concurrent use by multiple goroutines: downloads targeting
// the same directory serialize their extract/publish phase within this
// process.
func (f *Downloader) DownloadAsset(assetName, version, dest string) error {
	return f.DownloadAssetWithContext(context.Background(), assetName, version, dest)
}

// DownloadAssetWithContext downloads a specific asset from a release using the provided context and progress tracker.
// assetName is the name of the asset to download.
// version is the release version to download from (empty string for latest).
// dest is the destination directory for the downloaded asset.
//
// It is safe for concurrent use by multiple goroutines: downloads targeting
// the same directory serialize their extract/publish phase within this
// process.
func (f *Downloader) DownloadAssetWithContext(ctx context.Context, assetName, version, dest string) error {
	var url string
	var err error

	if version == "" {
		// Get latest version
		url, err = f.GetReleaseAssetURL(assetName)
		if err != nil {
			return err
		}
	} else {
		// Use specified version
		url = f.GetReleaseAssetURLByVersion(assetName, version)
	}

	return f.get(ctx, url, dest)
}

// DownloadLatestAsset downloads the latest asset that matches a pattern from the repository.
// pattern is a regex pattern to match against asset names.
// dest is the destination directory for the downloaded asset.
func (f *Downloader) DownloadLatestAsset(pattern string, dest string) error {
	rel, err := f.fetchLatestRelease()
	if err != nil {
		return err
	}

	// Find the first asset that matches the pattern; the tag from the same
	// API response is reused, so no second API call is needed.
	for _, asset := range rel.Assets {
		if matched, _ := regexp.MatchString(pattern, asset.Name); matched {
			return f.get(context.Background(), f.GetReleaseAssetURLByVersion(asset.Name, rel.TagName), dest)
		}
	}

	return errors.New("no matching asset found")
}

// GetLatestReleaseAssets returns a list of asset names from the latest release.
func (f *Downloader) GetLatestReleaseAssets() ([]string, error) {
	rel, err := f.fetchLatestRelease()
	if err != nil {
		return nil, err
	}

	assetNames := make([]string, len(rel.Assets))
	for i, asset := range rel.Assets {
		assetNames[i] = asset.Name
	}

	return assetNames, nil
}

// archiveKind classifies how a downloaded asset is handled, based on the
// file name suffix of the original request URL.
type archiveKind int

const (
	archiveNone archiveKind = iota
	archiveZip
	archiveTarGz
)

// archiveKindFor classifies u by the suffix of its path (query string
// stripped). Naming and archive detection always use the original request
// URL because GitHub release downloads redirect to a CDN URL whose path
// contains no asset name.
func archiveKindFor(u *url.URL) archiveKind {
	switch {
	case strings.HasSuffix(u.Path, ".zip"):
		return archiveZip
	case strings.HasSuffix(u.Path, ".tar.gz"), strings.HasSuffix(u.Path, ".tgz"):
		return archiveTarGz
	default:
		return archiveNone
	}
}

// destFileName derives the destination file name for a non-archive download
// from the basename of the URL path (query string stripped).
func destFileName(u *url.URL) (string, error) {
	name := path.Base(u.Path)
	if name == "." || name == "/" {
		return "", fmt.Errorf("download URL %q has no usable file name", u.String())
	}
	return name, nil
}

// stripTopLevel removes everything up to and including the first path
// separator, e.g. "llama-b1234/bin/llama-cli.dll" becomes
// "bin/llama-cli.dll". It returns the empty string for the top-level
// directory entry itself.
func stripTopLevel(name string) string {
	if idx := strings.Index(name, "/"); idx != -1 {
		name = name[idx+1:]
	}
	return name
}

// safeJoin joins a slash-separated archive entry name under root, rejecting
// entries that would escape root: traversal (".."), absolute paths, and
// Windows drive letters (zip-slip protection).
func safeJoin(root, name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") ||
		path.IsAbs(cleaned) || filepath.VolumeName(cleaned) != "" {
		return "", fmt.Errorf("archive entry %q escapes destination directory", name)
	}
	target := filepath.Join(root, filepath.FromSlash(cleaned))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("archive entry %q escapes destination directory", name)
	}
	return target, nil
}

// downloadTo streams rawURL into w with a GET request. The response body is
// wrapped by the configured ProgressTracker, which is the single progress
// hook for every download; a nil tracker (or a nil wrapped reader) falls
// back to the raw body.
func (f *Downloader) downloadTo(ctx context.Context, rawURL string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	f.setCommonHeaders(req)

	resp, err := f.createDownloadClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %q failed: HTTP status %d", rawURL, resp.StatusCode)
	}

	body := resp.Body
	if f.ProgressTracker != nil {
		if wrapped := f.ProgressTracker.TrackProgress(rawURL, 0, resp.ContentLength, resp.Body); wrapped != nil {
			body = wrapped
			defer body.Close()
		}
	}

	if _, err := io.Copy(w, body); err != nil {
		return fmt.Errorf("download %q failed: %w", rawURL, err)
	}

	// io.Copy may swallow a context cancellation that arrived with the final
	// EOF chunk; surface it explicitly.
	return ctx.Err()
}

// get downloads rawURL into dest. Archive assets (.zip, .tar.gz, .tgz) are
// extracted into dest with their top-level directory stripped; any other
// asset is saved as dest/<basename of the URL path>.
func (f *Downloader) get(ctx context.Context, rawURL, dest string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid download URL %q: %w", rawURL, err)
	}

	if err := os.MkdirAll(dest, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Stage the download in a hidden temp file inside dest (same volume, so
	// the final rename is atomic); a failed download never leaves partial
	// files with their final names behind.
	tmp, err := os.CreateTemp(dest, ".libfetch-download-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename

	err = f.downloadTo(ctx, rawURL, tmp)
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}

	// Serialize the extract/publish phase per destination so concurrent
	// downloads into the same directory do not interleave file writes or race
	// on the final rename. The download itself is not serialized: staging
	// file names are unique. (Separate namespace from the install lock — see
	// locks.go; sharing one would deadlock InstallAsset -> DownloadAsset.)
	unlock := downloadLocks.lock(lockKey(dest))
	defer unlock()

	switch archiveKindFor(u) {
	case archiveZip:
		return extractZip(tmpPath, dest)
	case archiveTarGz:
		return extractTarGz(tmpPath, dest)
	default:
		name, err := destFileName(u)
		if err != nil {
			return err
		}
		return os.Rename(tmpPath, filepath.Join(dest, name))
	}
}

// extractZip extracts the zip archive at src into dst, stripping the
// top-level directory of each entry.
func extractZip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("failed to open zip archive: %w", err)
	}
	defer r.Close()

	for _, zf := range r.File {
		// Skip the top-level directory itself
		name := stripTopLevel(zf.Name)
		if name == "" {
			continue
		}

		target, err := safeJoin(dst, name)
		if err != nil {
			return err
		}

		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, zf.Mode()); err != nil {
				return fmt.Errorf("failed to create directory %q: %w", zf.Name, err)
			}
			continue
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("failed to create parent directory: %w", err)
		}

		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("failed to open zip entry %q: %w", zf.Name, err)
		}

		// Symlink entries store their target as the contents
		if zf.Mode()&os.ModeSymlink != 0 {
			data, readErr := io.ReadAll(rc)
			rc.Close()
			if readErr != nil {
				return fmt.Errorf("failed to read zip entry %q: %w", zf.Name, readErr)
			}
			if err := os.Symlink(string(data), target); err != nil && !os.IsExist(err) {
				return fmt.Errorf("failed to create symlink %q: %w", zf.Name, err)
			}
			continue
		}

		out, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, zf.Mode().Perm())
		if err != nil {
			rc.Close()
			return fmt.Errorf("failed to create file %q: %w", zf.Name, err)
		}

		_, copyErr := io.Copy(out, rc)
		out.Close()
		rc.Close()
		if copyErr != nil {
			return fmt.Errorf("failed to extract %q: %w", zf.Name, copyErr)
		}
	}

	return nil
}

// extractTarGz extracts the tar.gz archive at src into dst, stripping the
// top-level directory of each entry.
func extractTarGz(src, dst string) error {
	archive, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open downloaded file: %w", err)
	}
	defer archive.Close()

	// Create gzip reader
	gzr, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzr.Close()

	// Create tar reader
	tr := tar.NewReader(gzr)

	// Extract files
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		// Skip the top-level directory itself (e.g., "llama-b1234/")
		name := stripTopLevel(header.Name)
		if name == "" {
			continue
		}

		target, err := safeJoin(dst, name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}
		case tar.TypeReg:
			// Ensure parent directory exists
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}

			// Create the file
			out, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create file: %w", err)
			}

			// Copy contents
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return fmt.Errorf("failed to write file: %w", err)
			}
			out.Close()
		case tar.TypeSymlink:
			// Handle symlinks
			if err := os.Symlink(header.Linkname, target); err != nil {
				// Ignore error if symlink already exists
				if !os.IsExist(err) {
					return fmt.Errorf("failed to create symlink: %w", err)
				}
			}
		}
	}

	return nil
}
