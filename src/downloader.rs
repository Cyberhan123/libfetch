use std::io;
use std::path::Path;
use std::sync::Arc;
use std::time::Duration;

use flate2::read::GzDecoder;
use reqwest::{Client, Proxy};
use serde::Deserialize;

/// Callback type for reporting download progress.
/// Arguments: source URL, bytes downloaded, total bytes, MiB/s, is_complete
pub type ProgressFn = Arc<dyn Fn(&str, u64, u64, f64, bool) + Send + Sync>;

/// Downloads GitHub release assets and queries release metadata.
pub struct Downloader {
    /// Number of times to retry fetching the latest version.
    pub retry_count: u32,
    /// Delay between retries.
    pub retry_delay: Duration,
    /// GitHub API URL for the latest release (pre-built from `repo`).
    pub api_url: String,
    /// GitHub repository in `owner/repo` format.
    pub repo: String,
    /// Optional HTTP proxy URL.
    pub proxy: Option<String>,
    /// Optional progress callback.
    pub progress: Option<ProgressFn>,
}

#[derive(Deserialize)]
struct ReleaseResponse {
    tag_name: String,
}

#[derive(Deserialize)]
struct ReleaseAssetsResponse {
    assets: Vec<AssetEntry>,
}

#[derive(Deserialize)]
struct AssetEntry {
    name: String,
}

impl Downloader {
    /// Create a downloader with default settings.
    pub fn new(repo: &str) -> Self {
        Self {
            retry_count: 3,
            retry_delay: Duration::from_secs(3),
            api_url: format!("https://api.github.com/repos/{repo}/releases/latest"),
            repo: repo.to_owned(),
            proxy: None,
            progress: None,
        }
    }

    /// Create a downloader with explicit configuration.
    pub fn with_config(
        repo: &str,
        retry_count: u32,
        retry_delay: Duration,
        proxy: Option<String>,
        progress: Option<ProgressFn>,
    ) -> Self {
        Self {
            retry_count,
            retry_delay,
            api_url: format!("https://api.github.com/repos/{repo}/releases/latest"),
            repo: repo.to_owned(),
            proxy,
            progress,
        }
    }

    /// Build an HTTP client, optionally with proxy support.
    fn build_client(&self) -> Result<Client, reqwest::Error> {
        let mut builder = Client::builder().timeout(Duration::from_secs(30)).user_agent(concat!("libfetch/", env!("CARGO_PKG_VERSION")));
        if let Some(proxy_url) = &self.proxy {
            builder = builder.proxy(Proxy::all(proxy_url)?);
        }
        builder.build()
    }

    /// Fetch the latest release tag from the GitHub API, retrying on failure.
    pub async fn latest_version(&self) -> Result<String, String> {
        let mut last_err = String::new();
        for _ in 0..self.retry_count {
            match self.get_latest_version().await {
                Ok(v) => return Ok(v),
                Err(e) => {
                    last_err = e;
                    tokio::time::sleep(self.retry_delay).await;
                }
            }
        }
        Err(format!("unable to fetch latest version: {last_err}"))
    }

    async fn get_latest_version(&self) -> Result<String, String> {
        let client = self.build_client().map_err(|e| e.to_string())?;
        let resp = client
            .get(&self.api_url)
            .header("Accept", "application/vnd.github+json")
            .header("X-GitHub-Api-Version", "2022-11-28")
            .send()
            .await
            .map_err(|e| e.to_string())?;

        if !resp.status().is_success() {
            let status = resp.status();
            let body = resp.text().await.unwrap_or_default();
            return Err(format!("GitHub API returned {status}: {body}"));
        }

        let release: ReleaseResponse = resp.json().await.map_err(|e| e.to_string())?;
        Ok(release.tag_name)
    }

    /// Return the download URL for a named asset of the latest release.
    pub async fn get_release_asset_url(&self, asset_name: &str) -> Result<String, String> {
        let version = self.latest_version().await?;
        Ok(self.get_release_asset_url_by_version(asset_name, &version))
    }

    /// Return the download URL for a named asset of a specific release version.
    pub fn get_release_asset_url_by_version(&self, asset_name: &str, version: &str) -> String {
        format!(
            "https://github.com/{}/releases/download/{}/{}",
            self.repo, version, asset_name
        )
    }

    /// List asset names for the latest release.
    pub async fn get_latest_release_assets(&self) -> Result<Vec<String>, String> {
        let client = self.build_client().map_err(|e| e.to_string())?;
        let resp = client
            .get(&self.api_url)
            .header("Accept", "application/vnd.github+json")
            .header("X-GitHub-Api-Version", "2022-11-28")
            .send()
            .await
            .map_err(|e| e.to_string())?;

        if !resp.status().is_success() {
            let status = resp.status();
            let body = resp.text().await.unwrap_or_default();
            return Err(format!("GitHub API returned {status}: {body}"));
        }

        let release: ReleaseAssetsResponse = resp.json().await.map_err(|e| e.to_string())?;
        Ok(release.assets.into_iter().map(|a| a.name).collect())
    }

    /// Download a named asset for the given version into `dest`.
    /// If `version` is empty, the latest version is used.
    pub async fn download_asset(
        &self,
        asset_name: &str,
        version: &str,
        dest: &str,
    ) -> Result<(), String> {
        let url = if version.is_empty() {
            self.get_release_asset_url(asset_name).await?
        } else {
            self.get_release_asset_url_by_version(asset_name, version)
        };

        self.fetch(&url, dest).await
    }

    /// Download the first asset whose name matches `pattern` (regex) from the latest release.
    pub async fn download_latest_asset(
        &self,
        pattern: &str,
        dest: &str,
    ) -> Result<(), String> {
        let re = regex::Regex::new(pattern).map_err(|e| e.to_string())?;
        let assets = self.get_latest_release_assets().await?;
        let name = assets
            .into_iter()
            .find(|a| re.is_match(a))
            .ok_or_else(|| "no matching asset found".to_owned())?;
        self.download_asset(&name, "", dest).await
    }

    /// Core download-and-extract routine.
    async fn fetch(&self, url: &str, dest: &str) -> Result<(), String> {
        if url.ends_with(".tar.gz") {
            self.download_and_extract_tar_gz(url, dest).await
        } else if url.ends_with(".zip") {
            self.download_and_extract_zip(url, dest).await
        } else {
            self.download_raw(url, dest).await
        }
    }

    /// Stream a file directly into `dest/<filename>` without extraction.
    async fn download_raw(&self, url: &str, dest: &str) -> Result<(), String> {
        use futures_util::StreamExt;

        let filename = url.split('/').next_back().unwrap_or("download");
        let dest_path = Path::new(dest).join(filename);
        std::fs::create_dir_all(dest).map_err(|e| e.to_string())?;

        let client = self.build_client().map_err(|e| e.to_string())?;
        let resp = client.get(url).send().await.map_err(|e| e.to_string())?;
        if !resp.status().is_success() {
            return Err(format!("download failed with status {}", resp.status()));
        }

        let total = resp.content_length().unwrap_or(0);
        let mut downloaded: u64 = 0;
        let mut stream = resp.bytes_stream();
        let mut file = std::fs::File::create(&dest_path).map_err(|e| e.to_string())?;

        let src = url.to_owned();
        let start = std::time::Instant::now();

        while let Some(chunk) = stream.next().await {
            let chunk = chunk.map_err(|e| e.to_string())?;
            downloaded += chunk.len() as u64;

            use std::io::Write;
            file.write_all(&chunk).map_err(|e| e.to_string())?;

            if let Some(progress) = &self.progress {
                let elapsed = start.elapsed().as_secs_f64();
                let mib_per_sec = if elapsed > 0.0 {
                    (downloaded as f64) / (1024.0 * 1024.0) / elapsed
                } else {
                    0.0
                };
                progress(&src, downloaded, total, mib_per_sec, false);
            }
        }

        if let Some(progress) = &self.progress {
            let elapsed = start.elapsed().as_secs_f64();
            let mib_per_sec = if elapsed > 0.0 {
                (downloaded as f64) / (1024.0 * 1024.0) / elapsed
            } else {
                0.0
            };
            progress(&src, downloaded, total, mib_per_sec, true);
        }

        Ok(())
    }

    /// Download a `.zip` archive and extract its contents into `dest`.
    async fn download_and_extract_zip(&self, url: &str, dest: &str) -> Result<(), String> {
        let bytes = self.download_bytes(url).await?;
        std::fs::create_dir_all(dest).map_err(|e| e.to_string())?;

        let cursor = std::io::Cursor::new(bytes);
        let mut archive = zip::ZipArchive::new(cursor).map_err(|e| e.to_string())?;

        for i in 0..archive.len() {
            let mut file = archive.by_index(i).map_err(|e| e.to_string())?;
            let outpath = Path::new(dest).join(file.mangled_name());

            if file.name().ends_with('/') {
                std::fs::create_dir_all(&outpath).map_err(|e| e.to_string())?;
            } else {
                if let Some(parent) = outpath.parent() {
                    std::fs::create_dir_all(parent).map_err(|e| e.to_string())?;
                }
                let mut out = std::fs::File::create(&outpath).map_err(|e| e.to_string())?;
                io::copy(&mut file, &mut out).map_err(|e| e.to_string())?;

                // Preserve executable permission on Unix
                #[cfg(unix)]
                {
                    use std::os::unix::fs::PermissionsExt;
                    if let Some(mode) = file.unix_mode() {
                        std::fs::set_permissions(&outpath, std::fs::Permissions::from_mode(mode))
                            .ok();
                    }
                }
            }
        }
        Ok(())
    }

    /// Download a `.tar.gz` archive and extract its contents into `dest`,
    /// stripping the top-level directory.
    async fn download_and_extract_tar_gz(&self, url: &str, dest: &str) -> Result<(), String> {
        let bytes = self.download_bytes(url).await?;
        std::fs::create_dir_all(dest).map_err(|e| e.to_string())?;

        let gz = GzDecoder::new(std::io::Cursor::new(bytes));
        let mut archive = tar::Archive::new(gz);

        for entry in archive.entries().map_err(|e| e.to_string())? {
            let mut entry = entry.map_err(|e| e.to_string())?;
            let path = entry.path().map_err(|e| e.to_string())?;

            // Strip top-level directory (e.g. "llama-b1234/")
            let stripped: std::path::PathBuf = path
                .components()
                .skip(1)
                .collect();

            if stripped.as_os_str().is_empty() {
                continue;
            }

            let outpath = Path::new(dest).join(&stripped);

            let header = entry.header();
            match header.entry_type() {
                tar::EntryType::Directory => {
                    std::fs::create_dir_all(&outpath).map_err(|e| e.to_string())?;
                }
                tar::EntryType::Symlink => {
                    if let Ok(Some(link)) = header.link_name() {
                        #[cfg(unix)]
                        std::os::unix::fs::symlink(&link, &outpath).ok();
                    }
                }
                _ => {
                    if let Some(parent) = outpath.parent() {
                        std::fs::create_dir_all(parent).map_err(|e| e.to_string())?;
                    }
                    entry.unpack(&outpath).map_err(|e| e.to_string())?;
                }
            }
        }
        Ok(())
    }

    /// Download URL into memory and return the bytes, reporting progress.
    async fn download_bytes(&self, url: &str) -> Result<bytes::Bytes, String> {
        use futures_util::StreamExt;

        let client = self.build_client().map_err(|e| e.to_string())?;
        let resp = client.get(url).send().await.map_err(|e| e.to_string())?;
        if !resp.status().is_success() {
            return Err(format!("download failed with status {}", resp.status()));
        }

        let total = resp.content_length().unwrap_or(0);
        let mut downloaded: u64 = 0;
        let mut chunks: Vec<bytes::Bytes> = Vec::new();
        let mut stream = resp.bytes_stream();

        let src = url.to_owned();
        let start = std::time::Instant::now();

        while let Some(chunk) = stream.next().await {
            let chunk = chunk.map_err(|e| e.to_string())?;
            downloaded += chunk.len() as u64;
            chunks.push(chunk);

            if let Some(progress) = &self.progress {
                let elapsed = start.elapsed().as_secs_f64();
                let mib_per_sec = if elapsed > 0.0 {
                    (downloaded as f64) / (1024.0 * 1024.0) / elapsed
                } else {
                    0.0
                };
                // Only report every ~100 MiB to avoid excessive output
                if downloaded % (100 * 1024 * 1024) < (chunks.last().map(|c| c.len()).unwrap_or(0) as u64) {
                    progress(&src, downloaded, total, mib_per_sec, false);
                }
            }
        }

        if let Some(progress) = &self.progress {
            let elapsed = start.elapsed().as_secs_f64();
            let mib_per_sec = if elapsed > 0.0 {
                (downloaded as f64) / (1024.0 * 1024.0) / elapsed
            } else {
                0.0
            };
            progress(&src, downloaded, total, mib_per_sec, true);
        }

        let total_bytes: bytes::Bytes = chunks.into_iter().flatten().collect();
        Ok(total_bytes)
    }
}
