use std::sync::Arc;

use crate::downloader::ProgressFn;

/// Returns the default progress function that prints download progress to stdout.
pub fn default_progress_fn() -> ProgressFn {
    Arc::new(|src: &str, current: u64, total: u64, mib_per_sec: f64, complete: bool| {
        if complete {
            println!(
                "\rdownloading {}... {:.0} MiB of {:.0} MiB ({:.2} MiB/s)",
                src,
                current as f64 / (1024.0 * 1024.0),
                total as f64 / (1024.0 * 1024.0),
                mib_per_sec
            );
        } else {
            print!(
                "\r\x1b[Kdownloading {}... {:.0} MiB of {:.0} MiB ({:.2} MiB/s)",
                src,
                current as f64 / (1024.0 * 1024.0),
                total as f64 / (1024.0 * 1024.0),
                mib_per_sec
            );
        }
    })
}

/// A convenience re-export so callers can write `DefaultProgressTracker()` to match
/// the Go API naming convention.
#[allow(non_snake_case)]
pub fn DefaultProgressTracker() -> ProgressFn {
    default_progress_fn()
}
