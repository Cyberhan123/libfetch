package libfetch

import (
	"os"
	"time"

	"github.com/hashicorp/go-getter"
)

// Api 结构体用于配置和执行下载安装操作
type Api struct {
	installDir      string
	progressTracker getter.ProgressTracker
	retryCount      int
	retryDelay      time.Duration
	proxy           string
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

// NewApi 创建新的 Api 实例，默认读取环境变量获取 HTTP 代理
func NewApi() *Api {
	// 读取环境变量获取 HTTP 代理
	proxy := os.Getenv("HTTP_PROXY")
	if proxy == "" {
		proxy = os.Getenv("HTTPS_PROXY")
	}

	return &Api{
		installDir:      ".",
		progressTracker: DefaultProgressTracker(),
		retryCount:      3,
		retryDelay:      3 * time.Second,
		proxy:           proxy,
	}
}

// SetInstallDir 设置安装目录
func (a *Api) SetInstallDir(dir string) *Api {
	a.installDir = dir
	return a
}

// SetProgressTracker 设置进度跟踪器
func (a *Api) SetProgressTracker(pt getter.ProgressTracker) *Api {
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
