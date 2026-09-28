package libfetch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type VersionInfo struct {
	TagName string `json:"tag_name"`
	Repo    string `json:"repo"`
}

// Install struct holds common variables for installation operations
type Install struct {
	versionFile string
	repo        string
	InstallPath string
	Downloader  *Downloader
}

// NewInstall creates a new Install instance with default values
func NewInstall(repo string, installPath string) *Install {
	return &Install{
		repo:        repo,
		versionFile: "version.json",
		InstallPath: installPath,
		Downloader:  NewDownloaderWithConfig(repo, 3, 3*time.Second, "", DefaultProgressTracker()),
	}
}

// InstallAsset installs the named asset of the given release into the
// install directory and records version.json.
//
// It is safe for concurrent use by multiple goroutines: installs targeting
// the same directory are serialized within this process. Cross-process
// coordination is NOT implemented.
func (i *Install) InstallAsset(assetName string, version string, allowUpgrade bool) error {
	// Serialize the whole check->download->extract->version-write sequence
	// per install directory so concurrent installs cannot interleave.
	unlock := installLocks.lock(lockKey(i.InstallPath))
	defer unlock()

	// Check if already installed
	if i.alreadyInstalled() {
		if !allowUpgrade {
			return nil
		}

		isLatest, latest, _, err := i.isLatestVersion()
		if err != nil {
			return fmt.Errorf("error checking version: %w", err)
		}

		if isLatest {
			return nil
		}

		return i.upgradeAsset(assetName, latest)
	}

	return i.initialInstallAsset(assetName, version)
}

func (i *Install) alreadyInstalled() bool {
	versionInfoPath := filepath.Join(i.InstallPath, i.versionFile)

	if _, err := os.Stat(versionInfoPath); err != nil {
		return false
	}

	return true
}

// isLatestVersion also returns the remote latest tag so callers can act on
// it without a second API call.
func (i *Install) isLatestVersion() (bool, string, *VersionInfo, error) {
	versionInfoPath := filepath.Join(i.InstallPath, i.versionFile)

	d, err := os.ReadFile(versionInfoPath)
	if err != nil {
		return false, "", nil, fmt.Errorf("error reading version info file: %w", err)
	}

	var versionInfo VersionInfo
	if err := json.Unmarshal(d, &versionInfo); err != nil {
		return false, "", nil, fmt.Errorf("error unmarshalling version info: %w", err)
	}

	// Verify the repo matches
	if versionInfo.Repo != i.repo {
		return false, "", nil, fmt.Errorf("installed version is for a different repository: %s", versionInfo.Repo)
	}

	latestVersion, err := i.Downloader.LatestVersion()
	if err != nil {
		return false, "", nil, fmt.Errorf("error getting latest version: %w", err)
	}

	return latestVersion == versionInfo.TagName, latestVersion, &versionInfo, nil
}

func (i *Install) initialInstallAsset(assetName string, version string) error {
	// Resolve "latest" up front so the download and the version file agree
	// and only one API call is spent.
	if len(version) == 0 {
		latest, err := i.Downloader.LatestVersion()
		if err != nil {
			return fmt.Errorf("error getting latest version: %w", err)
		}
		version = latest
	}

	// Download the asset
	if err := i.Downloader.DownloadAsset(assetName, version, i.InstallPath); err != nil {
		return fmt.Errorf("error downloading asset: %w", err)
	}

	return i.createVersionFile(version)
}

// upgradeAsset replaces the current installation with the named asset of the
// given release. It must be called with the install lock held.
func (i *Install) upgradeAsset(assetName, version string) error {
	// Refuse unsafe paths: the swap removes the whole install directory,
	// which must never be the working directory or a filesystem root.
	clean := filepath.Clean(i.InstallPath)
	if clean == "." || clean == "" || filepath.Dir(clean) == clean {
		return fmt.Errorf("refusing to upgrade unsafe install path %q", i.InstallPath)
	}

	// Stage the new release next to the install directory (same volume, so
	// the swap rename is atomic): a failed download leaves the current
	// installation untouched.
	staging, err := os.MkdirTemp(filepath.Dir(clean), ".libfetch-upgrade-*")
	if err != nil {
		return fmt.Errorf("error creating staging directory: %w", err)
	}
	defer os.RemoveAll(staging) // no-op after a successful rename

	if err := i.Downloader.DownloadAsset(assetName, version, staging); err != nil {
		return fmt.Errorf("error downloading asset: %w", err)
	}

	if err := os.RemoveAll(i.InstallPath); err != nil {
		return fmt.Errorf("error removing previous installation: %w", err)
	}

	if err := os.Rename(staging, i.InstallPath); err != nil {
		return fmt.Errorf("error swapping installation directory: %w", err)
	}

	return i.createVersionFile(version)
}

// CreateVersionFile creates a version info file in the specified directory.
// The file is written atomically (staged in a temp file, then renamed), so
// concurrent writers can never leave a torn version.json behind.
func (i *Install) CreateVersionFile(version string) error {
	// Ensure the directory exists
	if err := os.MkdirAll(i.InstallPath, 0755); err != nil {
		return fmt.Errorf("error creating install directory: %w", err)
	}

	versionInfo := VersionInfo{
		TagName: version,
		Repo:    i.repo,
	}

	d, err := json.Marshal(versionInfo)
	if err != nil {
		return fmt.Errorf("error marshalling version info: %w", err)
	}

	// Serialize write+rename per directory: on Windows, concurrent renames
	// over the same target fail with a sharing violation. (Separate
	// namespace from the install lock — the install flow calls this while
	// already holding it.)
	unlock := versionLocks.lock(lockKey(i.InstallPath))
	defer unlock()

	// Stage the write in a temp file inside the same directory (same volume,
	// so the final rename is atomic); a crash never leaves a torn file.
	tmp, err := os.CreateTemp(i.InstallPath, ".libfetch-version-*.tmp")
	if err != nil {
		return fmt.Errorf("error creating version info file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename

	if _, err := tmp.Write(d); err != nil {
		tmp.Close()
		return fmt.Errorf("error writing version info: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("error closing version info file: %w", err)
	}

	return os.Rename(tmpPath, filepath.Join(i.InstallPath, i.versionFile))
}

// createVersionFile is the internal version of CreateVersionFile.
func (i *Install) createVersionFile(version string) error {
	return i.CreateVersionFile(version)
}

// GetInstalledVersion returns the installed version information for the specified path.
func (i *Install) GetInstalledVersion() (*VersionInfo, error) {
	versionInfoPath := filepath.Join(i.InstallPath, i.versionFile)

	d, err := os.ReadFile(versionInfoPath)
	if err != nil {
		return nil, fmt.Errorf("error reading version info file: %w", err)
	}

	var versionInfo VersionInfo
	if err := json.Unmarshal(d, &versionInfo); err != nil {
		return nil, fmt.Errorf("error unmarshalling version info: %w", err)
	}

	return &versionInfo, nil
}
