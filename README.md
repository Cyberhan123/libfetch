# libfetch

A Rust library for downloading, comparing, and installing GitHub release assets with a chainable, builder-style async API.

## Installation

Add to your `Cargo.toml`:

```toml
[dependencies]
libfetch = { git = "https://github.com/Cyberhan123/libfetch" }
tokio = { version = "1", features = ["full"] }
```

## Quick Start

```rust
use libfetch::Api;

#[tokio::main]
async fn main() {
    // Create API instance with default settings
    let api = Api::new()
        .set_install_dir("./install")
        .set_retry_count(5)
        .set_retry_delay_secs(3)
        .set_proxy("http://proxy.example.com:8080");

    // Download and install the latest release
    api.repo("owner/repo")
        .latest()
        .install(|version| format!("asset-{version}.zip"))
        .await
        .expect("Installation failed");

    println!("Installation completed successfully!");

    // Check installed version
    let api2 = Api::new().set_install_dir("./install");
    let repo = api2.repo("owner/repo");
    let info = repo.get_installed_version().expect("Failed to read version");
    println!("Installed version: {}", info.tag_name);
}
```

## Configuration

### Environment Variables

- `HTTP_PROXY` – HTTP proxy URL (read automatically on `Api::new()`)
- `HTTPS_PROXY` – HTTPS proxy URL (used if `HTTP_PROXY` is not set)

### Builder Methods

| Method | Description |
|---|---|
| `.set_install_dir(dir)` | Set the installation directory (default: `"."`) |
| `.set_retry_count(n)` | Number of retries for GitHub API calls (default: `3`) |
| `.set_retry_delay_secs(s)` | Seconds between retries (default: `3`) |
| `.set_proxy(url)` | Override the HTTP/HTTPS proxy |
| `.no_progress()` | Disable progress output |

### Version examples

```rust
// Download latest release
api.repo("owner/repo").latest()
   .install(|v| format!("asset-{v}.zip"))
   .await?;

// Download a specific tag
api.repo("libffi/libffi").version("v3.5.1")
   .install(|v| {
       let clean = v.trim_start_matches('v');
       format!("libffi-{clean}-x86-32bit-msvc-binaries.zip")
   })
   .await?;
```

## Building & Testing

```bash
cargo build
cargo test                           # unit tests only
cargo test -- --include-ignored      # also run network integration tests
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

This project is licensed under the MIT License – see the LICENSE file for details.

---

Made with ❤️ in Rust
