# libfetch

A Go library for downloading, comparing, and installing GitHub release assets with a chainable API.

Zero external dependencies — implemented entirely with the Go standard library.

## Installation

```bash
go get -u github.com/Cyberhan123/libfetch
```

## Quick Start

```go
package main

import (
	"fmt"
	"os"

	"github.com/Cyberhan123/libfetch"
)

func main() {
	// Create API instance with default settings
	api := libfetch.NewApi()
	
	// Set installation directory and other options
	api.SetInstallDir("./install")
	api.SetRetryCount(5)
	api.SetRetryTimeDelay(3) // 3 seconds
	api.SetProxy("http://proxy.example.com:8080")
	api.SetToken(os.Getenv("GITHUB_TOKEN")) // optional: see Authentication
	
	// Download and install the latest release
	err := api.Repo("owner/repo").Latest().Install(func(version string) string {
		// Return asset name based on version
		return fmt.Sprintf("asset-%s.zip", version)
	})
	
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	
	fmt.Println("Installation completed successfully!")
	
	// Check installed version
	versionInfo, err := api.Repo("owner/repo").GetInstalledVersion()
	if err != nil {
		fmt.Printf("Error getting installed version: %v\n", err)
		return
	}
	
	fmt.Printf("Installed version: %s\n", versionInfo.TagName)
}
```


## Configuration

### Authentication

Without a token, GitHub's API allows only 60 requests per hour per IP. With a
personal access token the limit rises to 5,000 per hour, and private
repositories become downloadable.

Configure the token either way:

```go
api := libfetch.NewApi().SetToken("ghp_your_token_here")
```

or via environment variables (read automatically by `NewApi`):

- `GITHUB_TOKEN` - GitHub personal access token
- `GH_TOKEN` - used if `GITHUB_TOKEN` is not set (gh CLI convention)

`SetToken` takes precedence over the environment variables. The token is sent
as an `Authorization: Bearer` header on both API requests and asset downloads.

Retries are rate-limit aware: when GitHub responds with `403`/`429` rate
limit headers, libfetch waits according to `Retry-After` /
`X-RateLimit-Reset` (capped at 2 minutes) before retrying, and does not waste
retries on permanent errors such as `401` (bad token) or `404` (no such repo).

### Concurrency

All entry points are safe for concurrent use by multiple goroutines within
one process: installs and downloads targeting the same directory serialize
their write phases automatically, and the default progress tracker tracks
concurrent downloads independently. Version metadata (`version.json`) is
written atomically. Cross-process coordination is not implemented.

### Environment Variables

- `HTTP_PROXY` - HTTP proxy URL
- `HTTPS_PROXY` - HTTP proxy URL (used if HTTP_PROXY is not set)
- `GITHUB_TOKEN` - GitHub personal access token
- `GH_TOKEN` - GitHub personal access token (used if GITHUB_TOKEN is not set)

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

### Development Setup

1. Fork the repository
2. Clone your fork
3. Create a feature branch
4. Make changes
5. Run tests
6. Submit a Pull Request

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Support

If you encounter any issues or have questions, please open an issue on GitHub.

---

Made with ❤️ in Go
