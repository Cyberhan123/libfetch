use std::path::{Path, PathBuf};
use std::time::Duration;

use serde::{Deserialize, Serialize};

use crate::downloader::Downloader;
use crate::progress::default_progress_fn;

/// Version information stored in `version.json` alongside the installed files.
#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct VersionInfo {
    pub tag_name: String,
    pub repo: String,
}

/// Manages installation, version tracking, and upgrades for a GitHub release asset.
pub struct Install {
    version_file: String,
    repo: String,
    /// Directory where files are installed.
    pub install_path: String,
    /// Downloader used for HTTP operations.
    pub downloader: Downloader,
}

impl Install {
    /// Create a new `Install` with default settings.
    pub fn new(repo: &str, install_path: &str) -> Self {
        Self {
            repo: repo.to_owned(),
            version_file: "version.json".to_owned(),
            install_path: install_path.to_owned(),
            downloader: Downloader::with_config(
                repo,
                3,
                Duration::from_secs(3),
                None,
                Some(default_progress_fn()),
            ),
        }
    }

    /// Install `asset_name` at `version`.
    ///
    /// - If not already installed, performs a fresh download.
    /// - If `allow_upgrade` is `true` and the installed version is outdated, upgrades in place.
    pub async fn install_asset(
        &self,
        asset_name: &str,
        version: &str,
        allow_upgrade: bool,
    ) -> Result<(), String> {
        if self.already_installed() {
            if !allow_upgrade {
                return Ok(());
            }

            let (is_latest, version_info) = self.is_latest_version().await?;
            if is_latest {
                return Ok(());
            }

            return self.upgrade_asset(&version_info).await;
        }

        self.initial_install_asset(asset_name, version).await
    }

    fn already_installed(&self) -> bool {
        self.version_file_path().exists()
    }

    fn version_file_path(&self) -> PathBuf {
        Path::new(&self.install_path).join(&self.version_file)
    }

    /// Returns `(is_latest, installed_version_info)`.
    async fn is_latest_version(&self) -> Result<(bool, VersionInfo), String> {
        let raw = std::fs::read_to_string(self.version_file_path())
            .map_err(|e| format!("error reading version info file: {e}"))?;

        let version_info: VersionInfo = serde_json::from_str(&raw)
            .map_err(|e| format!("error parsing version info: {e}"))?;

        if version_info.repo != self.repo {
            return Err(format!(
                "installed version is for a different repository: {}",
                version_info.repo
            ));
        }

        let latest = self.downloader.latest_version().await
            .map_err(|e| format!("error getting latest version: {e}"))?;

        Ok((latest == version_info.tag_name, version_info))
    }

    async fn initial_install_asset(&self, asset_name: &str, version: &str) -> Result<(), String> {
        self.downloader
            .download_asset(asset_name, version, &self.install_path)
            .await
            .map_err(|e| format!("error downloading asset: {e}"))?;

        let resolved_version = if version.is_empty() {
            self.downloader.latest_version().await
                .map_err(|e| format!("error getting latest version: {e}"))?
        } else {
            version.to_owned()
        };

        self.create_version_file(&resolved_version)
    }

    async fn upgrade_asset(&self, _old_info: &VersionInfo) -> Result<(), String> {
        // Remove existing installation directory
        let p = Path::new(&self.install_path);
        if p.exists() {
            std::fs::remove_dir_all(p)
                .map_err(|e| format!("error removing old installation: {e}"))?;
        }

        // Fetch the newest version tag
        let latest = self.downloader.latest_version().await
            .map_err(|e| format!("error getting latest version: {e}"))?;

        // We need the asset name to download; store it in the version file approach
        // For the upgrade path the caller passes the assetName through InstallAsset,
        // so we use an empty string which triggers latest-version URL resolution.
        self.downloader
            .download_asset("", &latest, &self.install_path)
            .await
            .map_err(|e| format!("error downloading asset: {e}"))?;

        self.create_version_file(&latest)
    }

    /// Write (or overwrite) the `version.json` file.
    pub fn create_version_file(&self, version: &str) -> Result<(), String> {
        std::fs::create_dir_all(&self.install_path)
            .map_err(|e| format!("error creating install directory: {e}"))?;

        let info = VersionInfo {
            tag_name: version.to_owned(),
            repo: self.repo.clone(),
        };

        let json = serde_json::to_string(&info)
            .map_err(|e| format!("error serializing version info: {e}"))?;

        std::fs::write(self.version_file_path(), json)
            .map_err(|e| format!("error writing version info: {e}"))
    }

    /// Read back the stored version information.
    pub fn get_installed_version(&self) -> Result<VersionInfo, String> {
        let raw = std::fs::read_to_string(self.version_file_path())
            .map_err(|e| format!("error reading version info file: {e}"))?;

        serde_json::from_str(&raw)
            .map_err(|e| format!("error parsing version info: {e}"))
    }
}
