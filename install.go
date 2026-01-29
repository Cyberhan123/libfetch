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

func (i *Install) InstallAsset(assetName string, version string, allowUpgrade bool) error {
	// Check if already installed
	if i.alreadyInstalled() {
		if !allowUpgrade {
			return nil
		}

		isLatest, versionInfo, err := i.isLatestVersion()
		if err != nil {
			return fmt.Errorf("error checking version: %w", err)
		}

		if isLatest {
			return nil
		}

		return i.upgradeAsset(versionInfo)
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

func (i *Install) isLatestVersion() (bool, *VersionInfo, error) {
	versionInfoPath := filepath.Join(i.InstallPath, i.versionFile)

	d, err := os.ReadFile(versionInfoPath)
	if err != nil {
		return false, nil, fmt.Errorf("error reading version info file: %w", err)
	}

	var versionInfo VersionInfo
	if err := json.Unmarshal(d, &versionInfo); err != nil {
		return false, nil, fmt.Errorf("error unmarshalling version info: %w", err)
	}

	// Verify the repo matches
	if versionInfo.Repo != i.repo {
		return false, nil, fmt.Errorf("installed version is for a different repository: %s", versionInfo.Repo)
	}

	latestVersion, err := i.Downloader.LatestVersion()
	if err != nil {
		return false, nil, fmt.Errorf("error getting latest version: %w", err)
	}

	return latestVersion == versionInfo.TagName, &versionInfo, nil
}

func (i *Install) initialInstallAsset(assetName string, version string) error {
	// Download the asset
	if err := i.Downloader.DownloadAsset(assetName, version, i.InstallPath); err != nil {
		return fmt.Errorf("error downloading asset: %w", err)
	}
	var innerVersion string

	if len(version) == 0 {
		var err error
		// Get latest version and create version file
		innerVersion, err = i.Downloader.LatestVersion()
		if err != nil {
			return fmt.Errorf("error getting latest version: %w", err)
		}
	} else {
		innerVersion = version
	}

	return i.createVersionFile(innerVersion)
}

func (i *Install) upgradeAsset(versionInfo *VersionInfo) error {
	// Clean up existing installation
	if _, err := os.Stat(i.InstallPath); !os.IsNotExist(err) {
		os.RemoveAll(i.InstallPath)
	}

	// Download the latest version
	if err := i.Downloader.DownloadAsset(versionInfo.TagName, "", i.InstallPath); err != nil {
		return fmt.Errorf("error downloading asset: %w", err)
	}

	// Get latest version and update version file
	version, err := i.Downloader.LatestVersion()
	if err != nil {
		return fmt.Errorf("error getting latest version: %w", err)
	}

	return i.createVersionFile(version)
}

// CreateVersionFile creates a version info file in the specified directory.
func (i *Install) CreateVersionFile(version string) error {
	// Ensure the directory exists
	if err := os.MkdirAll(i.InstallPath, 0755); err != nil {
		return fmt.Errorf("error creating install directory: %w", err)
	}

	versionInfoPath := filepath.Join(i.InstallPath, i.versionFile)

	f, err := os.Create(versionInfoPath)
	if err != nil {
		return fmt.Errorf("error creating version info file: %w", err)
	}
	defer f.Close()

	versionInfo := VersionInfo{
		TagName: version,
		Repo:    i.repo,
	}

	d, err := json.Marshal(versionInfo)
	if err != nil {
		return fmt.Errorf("error marshalling version info: %w", err)
	}

	if _, err := f.Write(d); err != nil {
		return fmt.Errorf("error writing version info: %w", err)
	}

	return nil
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
