package libfetch_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Cyberhan123/libfetch"
)

func TestDownLoadLLAMA(t *testing.T) {
	api := libfetch.NewApi()
	api.SetInstallDir("./llamalib")
	// 下载最新版本
	err := api.Repo("ggml-org/llama.cpp").Latest().Install(func(version string) string {
		return fmt.Sprintf("llama-%s-bin-win-cpu-x64.zip", version)
	})
	if err != nil {
		t.Fatalf("error installing llama.cpp: %v", err)
	}
	t.Log("llama.cpp installed successfully")
}

func TestDownLoadSD(t *testing.T) {
	// 初始化的时候默认读取环境变量获取http代理
	api := libfetch.NewApi()
	// 显示下载进度条
	api.SetProgressTracker(libfetch.DefaultProgressTracker())
	// 设置重试次数。如果不设置则默认为3
	api.SetRetryCount(3)
	// 设置重试延迟时间。如果不设置则默认为3秒
	api.SetRetryTimeDelay(3)
	// 设置安装目录
	api.SetInstallDir("./sd")

	// 通过 version文件判断版本是否一致， 如果版本不一致，则下载当前版本替换到当前目录，如果一致则不进行下载，下载完成后会进行解压安装，并在目录中留下version文件
	err := api.Repo("leejet/stable-diffusion.cpp").Latest().Install(func(version string) string {
		cleanVersion := strings.TrimPrefix(version, "master-487-")
		return fmt.Sprintf("sd-master-%s-bin-win-avx2-x64.zip", cleanVersion)
	})
	if err != nil {
		t.Fatalf("error installing sd-master: %v", err)
	}

	t.Log("sd-master installed successfully")
}

func TestDownLoadLibFFI(t *testing.T) {
	libffiapi := libfetch.NewApi()
	libffiapi.SetInstallDir("./libffi")
	// 如果版本不一致，则下载当前版本替换到当前目录，如果一致则不进行下载
	err := libffiapi.Repo("libffi/libffi").Version("v3.5.1").Install(func(version string) string {
		cleanVersion := strings.TrimPrefix(version, "v")
		return fmt.Sprintf("libffi-%s-x86-32bit-msvc-binaries.zip", cleanVersion)
	})
	if err != nil {
		t.Fatalf("error installing libffi: %v", err)
	}
	t.Log("libffi installed successfully")
}
