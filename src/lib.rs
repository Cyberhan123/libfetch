//! # libfetch
//!
//! A Rust library for downloading, comparing, and installing GitHub release assets
//! with a chainable, builder-style API.
//!
//! ## Quick Start
//!
//! ```rust,no_run
//! use libfetch::Api;
//!
//! #[tokio::main]
//! async fn main() {
//!     let api = Api::new();
//!     api.set_install_dir("./llamalib")
//!        .repo("ggml-org/llama.cpp")
//!        .latest()
//!        .install(|version| format!("llama-{version}-bin-win-cpu-x64.zip"))
//!        .await
//!        .unwrap();
//! }
//! ```

pub mod api;
pub mod downloader;
pub mod install;
pub mod progress;

pub use api::Api;
pub use downloader::Downloader;
pub use install::{Install, VersionInfo};
pub use progress::DefaultProgressTracker;
