# Project Guidelines

Use this as the repo-wide AI reference for architecture boundaries, workflow, and documentation ownership. Keep it short; module-specific details belong in the nearest `README.md`.

## Project Overview

libfetch is a zero-dependency Go library for downloading, extracting, and installing GitHub release assets through a chainable API: `NewApi().Repo("owner/repo").Latest().Install(assetFunc)`. It uses only the Go standard library — `go.mod` has no `require` directives; keep it that way.

## Commands

**Never run bare `go test ./...` casually**: `api_test.go` (package `libfetch_test`) hits real GitHub and downloads large real assets (llama.cpp, stable-diffusion.cpp). All other tests run against local `httptest` servers and need no network — select them with `-run`.

Code comments are written in English (some older comments are Chinese — do not add new Chinese comments).

## Architecture

Three layers, all in package `libfetch`:

1. **api.go — chainable facade.** `Api` holds configuration (install dir, retry count/delay, proxy, token, `ProgressTracker`); every setter returns `*Api` for chaining. `Repo()` → `RepoApi` → `Latest()`/`Version()` → `VersionApi.Install(assetFunc)`. `NewApi` reads `HTTP_PROXY`/`HTTPS_PROXY` and `GITHUB_TOKEN`/`GH_TOKEN` from the environment; explicit setters override both. The token is sent as `Authorization: Bearer` on both API calls and asset downloads (Go's http.Client strips it on cross-host redirects to the CDN, which is intended).

2. **install.go — install state + version tracking.** Installed state lives in `version.json` (tag + repo) inside the install dir. `InstallAsset`: already installed and not upgrading → no-op; installed and already latest → no-op; otherwise download/extract and write `version.json`. Upgrades stage the new release into a sibling temp dir, then `RemoveAll(old)` + `Rename(staging, install)` for an atomic same-volume swap; unsafe install paths (`.`, filesystem roots) are refused.

3. **download.go — GitHub API + asset download.** `fetchLatestRelease` parses tag + asset names with rate-limit-aware retry (`statusError.retryable()`: rate limits and 5xx are retried, other 4xx like 401/404 fail fast; retry waits follow `Retry-After`/`X-RateLimit-Reset`, capped at 2 minutes). `get()` stages every download in a hidden temp file inside the destination, then either extracts archives (`.zip`, `.tar.gz`, `.tgz` — top-level directory stripped, zip-slip protection via `safeJoin`) or renames to the URL basename. Destination file names always derive from the **original request URL**, never the redirect target — GitHub redirects to CDN URLs whose paths contain no asset name.

### Locking (locks.go) — the invariant that spans files

Process-local per-directory mutual exclusion via three **independent** `keyedMutex` namespaces, keyed by absolute, lowercased-on-Windows path:

- `installLocks` — held for the whole check→download→extract→version-write sequence
- `downloadLocks` — held for the extract/publish phase of a download
- `versionLocks` — held around `version.json` write+rename (concurrent renames on Windows fail with sharing violations)

These MUST remain separate namespaces: `InstallAsset` holds the install lock and then descends into `DownloadAsset` (download lock, same directory) and `CreateVersionFile` (version lock); one shared namespace would self-deadlock because `sync.Mutex` is not reentrant. Cross-process locking is explicitly not implemented.

### progress.go

The `ProgressTracker` interface is deliberately method-set-compatible with `getter.ProgressTracker` from hashicorp/go-getter (this library is a dependency-free rewrite of a go-getter-based tool — existing tracker implementations must keep working). The default tracker hands out an independent `progressReader` per download so a single tracker safely tracks concurrent downloads.

## Testing Conventions

- Network-free tests point a `Downloader` at `httptest` servers via the exported `ApiURL` and the **unexported `assetBaseURL`** field (which exists solely so tests can redirect asset downloads to a local server). See `newFakeGitHub`/`newFakeInstall` in `install_test.go` — a local server mimicking both GitHub endpoints (latest-release API + asset download).
- `assertNoTempResidue` (download_test.go) verifies no staging temp files (`.libfetch-download-*`, `.libfetch-version-*`, `.libfetch-upgrade-*`) leak after failed operations — reuse it in tests that exercise the staging/upgrade paths.
- Symlink assertions skip gracefully where symlinks are unavailable (Windows without Developer Mode/admin) — follow that pattern via `canSymlink`.
