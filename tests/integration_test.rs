/// Integration tests that mirror the Go test suite.
///
/// These tests make real network requests to the GitHub API.
/// Run with `cargo test -- --include-ignored` to include network tests.
#[cfg(test)]
mod tests {
    use libfetch::Api;

    /// Mirrors `TestDownLoadLLAMA` in Go.
    #[tokio::test]
    #[ignore = "requires network access and downloads large files"]
    async fn test_download_llama() {
        let result = Api::new()
            .set_install_dir("./llamalib")
            .repo("ggml-org/llama.cpp")
            .latest()
            .install(|version| format!("llama-{version}-bin-win-cpu-x64.zip"))
            .await;

        assert!(result.is_ok(), "error installing llama.cpp: {:?}", result);
    }

    /// Mirrors `TestDownLoadSD` in Go.
    #[tokio::test]
    #[ignore = "requires network access and downloads large files"]
    async fn test_download_sd() {
        let result = Api::new()
            .set_install_dir("./sd")
            .set_retry_count(3)
            .set_retry_delay_secs(3)
            .repo("leejet/stable-diffusion.cpp")
            .latest()
            .install(|version| {
                let clean = version.trim_start_matches("master-487-");
                format!("sd-master-{clean}-bin-win-avx2-x64.zip")
            })
            .await;

        assert!(result.is_ok(), "error installing stable-diffusion.cpp: {:?}", result);
    }

    /// Mirrors `TestDownLoadLibFFI` in Go.
    #[tokio::test]
    #[ignore = "requires network access and downloads large files"]
    async fn test_download_libffi() {
        let result = Api::new()
            .set_install_dir("./libffi")
            .repo("libffi/libffi")
            .version("v3.5.1")
            .install(|version| {
                let clean = version.trim_start_matches('v');
                format!("libffi-{clean}-x86-32bit-msvc-binaries.zip")
            })
            .await;

        assert!(result.is_ok(), "error installing libffi: {:?}", result);
    }

    /// Unit test: `get_release_asset_url_by_version` produces the expected URL.
    #[test]
    fn test_asset_url_by_version() {
        let dl = libfetch::Downloader::new("owner/repo");
        let url = dl.get_release_asset_url_by_version("asset.zip", "v1.0.0");
        assert_eq!(
            url,
            "https://github.com/owner/repo/releases/download/v1.0.0/asset.zip"
        );
    }

    /// Unit test: version file round-trip (write then read).
    #[test]
    fn test_version_file_roundtrip() {
        let dir = std::env::temp_dir().join("libfetch_test_vf");
        let dir_str = dir.to_str().unwrap();

        let install = libfetch::Install::new("owner/repo", dir_str);
        install.create_version_file("v2.0.0").expect("create version file");

        let info = install.get_installed_version().expect("read version file");
        assert_eq!(info.tag_name, "v2.0.0");
        assert_eq!(info.repo, "owner/repo");

        // Cleanup
        let _ = std::fs::remove_dir_all(&dir);
    }
}
