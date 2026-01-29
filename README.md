# libfetch

A Go library for downloading, comparing, and installing GitHub release assets with a chainable API.

## Installation

```bash
go get -u github.com/cyberhan123/libfetch
```

## Quick Start

```go
package main

import (
	"fmt"
	"github.com/cyberhan123/libfetch"
)

func main() {
	// Create API instance with default settings
	api := libfetch.NewApi()
	
	// Set installation directory and other options
	api.SetInstallDir("./install")
	api.SetRetryCount(5)
	api.SetRetryTimeDelay(3) // 3 seconds
	api.SetProxy("http://proxy.example.com:8080")
	
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

### Environment Variables

- `HTTP_PROXY` - HTTP proxy URL
- `HTTPS_PROXY` - HTTPS proxy URL (used if HTTP_PROXY is not set)

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
