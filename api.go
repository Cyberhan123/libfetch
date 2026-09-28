package libfetch

import (
	"os"
	"time"
)

// Api is used to configure and execute download/install operations.
type Api struct {
	installDir      string
	progressTracker ProgressTracker
	retryCount      int
	retryDelay      time.Duration
	proxy           string
	token           string
}

// RepoApi 结构体用于指定 GitHub 仓库
type RepoApi struct {
	api  *Api
	repo string
}

// VersionApi 结构体用于指定版本
type VersionApi struct {
	api      *Api
	repo     string
	version  string
	isLatest bool
}

// NewApi creates a new Api instance. It reads the HTTP proxy from the
// HTTP_PROXY/HTTPS_PROXY environment variables and the GitHub token from
// GITHUB_TOKEN/GH_TOKEN; explicit setters override both.
func NewApi() *Api {
	// Read the HTTP proxy from the environment
	proxy := os.Getenv("HTTP_PROXY")
	if proxy == "" {
		proxy = os.Getenv("HTTPS_PROXY")
	}

	// Read the GitHub token from the environment (gh CLI convention)
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}

	return &Api{
		installDir:      ".",
		progressTracker: DefaultProgressTracker(),
		retryCount:      3,
		retryDelay:      3 * time.Second,
		proxy:           proxy,
		token:           token,
	}
}

// SetInstallDir 设置安装目录
func (a *Api) SetInstallDir(dir string) *Api {
	a.installDir = dir
	return a
}

// SetProgressTracker sets the progress tracker.
func (a *Api) SetProgressTracker(pt ProgressTracker) *Api {
	a.progressTracker = pt
	return a
}

// SetRetryCount 设置重试次数
func (a *Api) SetRetryCount(count int) *Api {
	a.retryCount = count
	return a
}

// SetRetryTimeDelay 设置重试延迟时间（秒）
func (a *Api) SetRetryTimeDelay(seconds int) *Api {
	a.retryDelay = time.Duration(seconds) * time.Second
	return a
}

// SetProxy 设置 HTTP 代理
func (a *Api) SetProxy(proxy string) *Api {
	a.proxy = proxy
	return a
}

// SetToken sets the GitHub personal access token used for API requests and
// asset downloads. It takes precedence over the GITHUB_TOKEN and GH_TOKEN
// environment variables.
func (a *Api) SetToken(token string) *Api {
	a.token = token
	return a
}

// Repo 设置 GitHub 仓库，返回 RepoApi
func (a *Api) Repo(repo string) *RepoApi {
	return &RepoApi{
		api:  a,
		repo: repo,
	}
}

// GetInstalledVersion 获取已安装的版本信息
func (r *RepoApi) GetInstalledVersion() (*VersionInfo, error) {
	install := NewInstall(r.repo, r.api.installDir)
	return install.GetInstalledVersion()
}

// Latest 设置为下载最新版本，返回 VersionApi
func (r *RepoApi) Latest() *VersionApi {
	return &VersionApi{
		api:      r.api,
		repo:     r.repo,
		isLatest: true,
	}
}

// Version 设置具体版本，返回 VersionApi
func (r *RepoApi) Version(version string) *VersionApi {
	return &VersionApi{
		api:      r.api,
		repo:     r.repo,
		version:  version,
		isLatest: false,
	}
}

// Install 安装指定的资产
// assetFunc 是一个回调函数，根据版本号生成资产文件名
func (v *VersionApi) Install(assetFunc func(version string) string) error {
	// 创建下载器，传递所有配置
	downloader := NewDownloaderWithConfig(v.repo, v.api.retryCount, v.api.retryDelay, v.api.proxy, v.api.progressTracker)
	downloader.Token = v.api.token

	var version string
	var err error

	if v.isLatest {
		version, err = downloader.LatestVersion()
		if err != nil {
			return err
		}
	} else {
		version = v.version
	}

	// 生成资产文件名
	assetName := assetFunc(version)
	//https://github.com/libffi/libffi/releases/download/v3.5.1/libffi-3.5.1-x86-32bit-msvc-binaries.zip
	// 创建 Install 实例并安装资产
	install := NewInstall(v.repo, v.api.installDir)
	install.Downloader = downloader
	return install.InstallAsset(assetName, version, v.isLatest)
}
