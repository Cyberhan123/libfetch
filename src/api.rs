use std::time::Duration;

use crate::downloader::{Downloader, ProgressFn};
use crate::install::{Install, VersionInfo};
use crate::progress::default_progress_fn;

// ──────────────────────────────────────────────────────────────────────────────
// Api
// ──────────────────────────────────────────────────────────────────────────────

/// Top-level entry-point with a chainable builder API.
///
/// # Example
/// ```rust,no_run
/// use libfetch::Api;
///
/// #[tokio::main]
/// async fn main() {
///     Api::new()
///         .set_install_dir("./out")
///         .repo("owner/repo")
///         .latest()
///         .install(|v| format!("asset-{v}.zip"))
///         .await
///         .unwrap();
/// }
/// ```
pub struct Api {
    install_dir: String,
    retry_count: u32,
    retry_delay: Duration,
    proxy: Option<String>,
    progress: Option<ProgressFn>,
}

impl Api {
    /// Create a new `Api` with sensible defaults.
    ///
    /// Proxy is read from `HTTP_PROXY` / `HTTPS_PROXY` environment variables
    /// (same behaviour as the Go version).
    pub fn new() -> Self {
        let proxy = std::env::var("HTTP_PROXY")
            .ok()
            .filter(|s| !s.is_empty())
            .or_else(|| std::env::var("HTTPS_PROXY").ok().filter(|s| !s.is_empty()));

        Self {
            install_dir: ".".to_owned(),
            retry_count: 3,
            retry_delay: Duration::from_secs(3),
            proxy,
            progress: Some(default_progress_fn()),
        }
    }

    /// Set the installation directory (builder).
    pub fn set_install_dir(mut self, dir: &str) -> Self {
        self.install_dir = dir.to_owned();
        self
    }

    /// Override the progress callback (builder).
    pub fn set_progress(mut self, progress: ProgressFn) -> Self {
        self.progress = Some(progress);
        self
    }

    /// Disable progress output (builder).
    pub fn no_progress(mut self) -> Self {
        self.progress = None;
        self
    }

    /// Set the number of retries when fetching the latest version (builder).
    pub fn set_retry_count(mut self, count: u32) -> Self {
        self.retry_count = count;
        self
    }

    /// Set the retry delay in seconds (builder).
    pub fn set_retry_delay_secs(mut self, secs: u64) -> Self {
        self.retry_delay = Duration::from_secs(secs);
        self
    }

    /// Set an explicit HTTP/HTTPS proxy URL (builder).
    pub fn set_proxy(mut self, proxy: &str) -> Self {
        self.proxy = Some(proxy.to_owned());
        self
    }

    /// Select a GitHub repository and return a [`RepoApi`].
    pub fn repo(self, repo: &str) -> RepoApi {
        RepoApi {
            api: self,
            repo: repo.to_owned(),
        }
    }
}

impl Default for Api {
    fn default() -> Self {
        Self::new()
    }
}

// ──────────────────────────────────────────────────────────────────────────────
// RepoApi
// ──────────────────────────────────────────────────────────────────────────────

/// Intermediate builder after a repository has been specified.
pub struct RepoApi {
    api: Api,
    repo: String,
}

impl RepoApi {
    /// Target the latest release.
    pub fn latest(self) -> VersionApi {
        VersionApi {
            api: self.api,
            repo: self.repo,
            version: String::new(),
            is_latest: true,
        }
    }

    /// Target a specific release version (e.g. `"v3.5.1"`).
    pub fn version(self, version: &str) -> VersionApi {
        VersionApi {
            api: self.api,
            repo: self.repo,
            version: version.to_owned(),
            is_latest: false,
        }
    }

    /// Return the installed [`VersionInfo`] from the install directory.
    pub fn get_installed_version(&self) -> Result<VersionInfo, String> {
        let install = Install::new(&self.repo, &self.api.install_dir);
        install.get_installed_version()
    }
}

// ──────────────────────────────────────────────────────────────────────────────
// VersionApi
// ──────────────────────────────────────────────────────────────────────────────

/// Intermediate builder after a version strategy has been chosen.
pub struct VersionApi {
    api: Api,
    repo: String,
    version: String,
    is_latest: bool,
}

impl VersionApi {
    /// Download and install the asset.
    ///
    /// `asset_fn` receives the resolved version string and must return the
    /// asset filename (e.g. `"llama-b1234-bin-win-cpu-x64.zip"`).
    pub async fn install<F>(self, asset_fn: F) -> Result<(), String>
    where
        F: FnOnce(&str) -> String,
    {
        let downloader = Downloader::with_config(
            &self.repo,
            self.api.retry_count,
            self.api.retry_delay,
            self.api.proxy.clone(),
            self.api.progress.clone(),
        );

        // Resolve version
        let version = if self.is_latest {
            downloader.latest_version().await?
        } else {
            self.version.clone()
        };

        let asset_name = asset_fn(&version);

        let mut install = Install::new(&self.repo, &self.api.install_dir);
        install.downloader = downloader;
        install.install_asset(&asset_name, &version, self.is_latest).await
    }
}
