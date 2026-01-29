package libfetch

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/go-getter"
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
	ProgressTracker getter.ProgressTracker
}

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

func NewDownloaderWithConfig(repo string, retryCount int, retryDelay time.Duration, proxy string, progressTracker getter.ProgressTracker) *Downloader {
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
	var version string
	var err error
	for range f.RetryCount {
		version, err = f.getLatestVersion()
		if err == nil {
			return version, nil
		}
		time.Sleep(f.RetryDelay)
	}

	return "", errors.New("unable to fetch latest version")
}

func (f *Downloader) getLatestVersion() (string, error) {
	req, err := http.NewRequest("GET", f.ApiURL, nil)
	if err != nil {
		return "", err
	}

	// Set required headers for GitHub API
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	// Create HTTP client with proxy support
	client := f.createHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("received status code %d from GitHub API: %s", resp.StatusCode, string(body))
	}

	var result struct {
		TagName string `json:"tag_name"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	return result.TagName, nil
}

// createHTTPClient creates an HTTP client with proxy support if configured
func (f *Downloader) createHTTPClient() *http.Client {
	transport := &http.Transport{}

	// Set proxy if configured
	if f.Proxy != "" {
		proxyURL, err := url.Parse(f.Proxy)
		if err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}

	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}
}

// GetReleaseAssetURL returns the download URL for a specific asset in the latest release.
func (f *Downloader) GetReleaseAssetURL(assetName string) (string, error) {
	// First get the latest version
	version, err := f.LatestVersion()
	if err != nil {
		return "", err
	}

	// Construct the download URL
	baseURL := fmt.Sprintf("https://github.com/%s/releases/download/%s", f.Repo, version)
	return fmt.Sprintf("%s/%s", baseURL, assetName), nil
}

// GetReleaseAssetURLByVersion returns the download URL for a specific asset in a specific release version.
func (f *Downloader) GetReleaseAssetURLByVersion(assetName, version string) string {
	baseURL := fmt.Sprintf("https://github.com/%s/releases/download/%s", f.Repo, version)
	return fmt.Sprintf("%s/%s", baseURL, assetName)
}

// DownloadAsset downloads a specific asset from the latest release of the repository.
// assetName is the name of the asset to download.
// dest is the destination directory for the downloaded asset.
func (f *Downloader) DownloadAsset(assetName, version, dest string) error {
	return f.DownloadAssetWithContext(context.Background(), assetName, version, dest)
}

// DownloadAssetWithContext downloads a specific asset from a release using the provided context and progress tracker.
// assetName is the name of the asset to download.
// version is the release version to download from (empty string for latest).
// dest is the destination directory for the downloaded asset.
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
	assets, err := f.GetLatestReleaseAssets()
	if err != nil {
		return err
	}

	// Find the first asset that matches the pattern
	for _, asset := range assets {
		if matched, _ := regexp.MatchString(pattern, asset); matched {
			return f.DownloadAsset(asset, "", dest)
		}
	}

	return errors.New("no matching asset found")
}

// GetLatestReleaseAssets returns a list of asset names from the latest release.
func (f *Downloader) GetLatestReleaseAssets() ([]string, error) {
	req, err := http.NewRequest("GET", f.ApiURL, nil)
	if err != nil {
		return nil, err
	}

	// Set required headers for GitHub API
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	// Create HTTP client with proxy support
	client := f.createHTTPClient()
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("received status code %d from GitHub API: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Assets []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	assetNames := make([]string, len(result.Assets))
	for i, asset := range result.Assets {
		assetNames[i] = asset.Name
	}

	return assetNames, nil
}

func (f *Downloader) setGetterClient(ctx context.Context, url, dest string) *getter.Client {
	myHttpGetter := &getter.HttpGetter{
		Client: f.createHTTPClient(),
	}
	client := &getter.Client{
		Ctx:              ctx,
		Src:              url,
		Dst:              dest,
		Mode:             getter.ClientModeAny,
		ProgressListener: f.ProgressTracker,
		Getters: map[string]getter.Getter{
			"http":  myHttpGetter,
			"https": myHttpGetter,
		},
	}

	if f.ProgressTracker != nil {
		client.ProgressListener = f.ProgressTracker
	}
	return client
}

func (f *Downloader) get(ctx context.Context, url, dest string) error {
	// Check if it's a .tar.gz file
	if strings.HasSuffix(url, ".tar.gz") {
		return f.downloadAndExtractTarGz(ctx, url, dest)
	}
	client := f.setGetterClient(ctx, url, dest)
	if err := client.Get(); err != nil {
		return err
	}

	return nil
}

// downloadAndExtractTarGz downloads a .tar.gz file and extracts it to the destination directory.
func (f *Downloader) downloadAndExtractTarGz(ctx context.Context, url, dest string) error {
	downloadFile := filepath.Join(dest, filepath.Base(url))

	client := f.setGetterClient(ctx, url+"?archive=false", dest)

	if err := client.Get(); err != nil {
		return err
	}
	defer os.Remove(downloadFile)

	resp, err := os.Open(downloadFile)
	if err != nil {
		return fmt.Errorf("failed to open downloaded file: %w", err)
	}
	defer resp.Close()

	// Create gzip reader
	gzr, err := gzip.NewReader(resp)
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

		// Strip the top-level directory (e.g., "llama-b1234/")
		name := header.Name
		if idx := strings.Index(name, "/"); idx != -1 {
			name = name[idx+1:]
		}

		// Skip empty names (the top-level directory itself)
		if name == "" {
			continue
		}

		target := filepath.Join(dest, filepath.Clean(name))

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
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create file: %w", err)
			}

			// Copy contents
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("failed to write file: %w", err)
			}
			f.Close()
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
