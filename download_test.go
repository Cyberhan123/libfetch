package libfetch

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// newTestDownloader returns a Downloader suitable for local tests: no proxy
// and a caller-supplied (possibly nil) progress tracker.
func newTestDownloader(pt ProgressTracker) *Downloader {
	return &Downloader{ProgressTracker: pt}
}

// serveBytes serves content with an explicit Content-Length so responses are
// not chunked (bodies above net/http's internal buffer size otherwise are).
func serveBytes(content []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		_, _ = w.Write(content)
	})
}

// assertNoTempResidue fails the test if any staging temp file is left in dir.
func assertNoTempResidue(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		for _, prefix := range []string{".libfetch-download-", ".libfetch-version-", ".libfetch-upgrade-"} {
			if strings.HasPrefix(e.Name(), prefix) {
				t.Errorf("temp file left behind: %s", e.Name())
			}
		}
	}
}

// canSymlink reports whether symlink creation works on this system (it
// requires Developer Mode or admin privileges on Windows).
func canSymlink(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink("target", filepath.Join(dir, "probe")); err != nil {
		t.Logf("symlinks not available on this system, skipping symlink assertion: %v", err)
		return false
	}
	return true
}

func TestGetPlainFile(t *testing.T) {
	const content = "hello libfetch"
	srv := httptest.NewServer(serveBytes([]byte(content)))
	defer srv.Close()

	dest := t.TempDir()
	if err := newTestDownloader(nil).get(context.Background(), srv.URL+"/files/hello.txt", dest); err != nil {
		t.Fatalf("get: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "hello.txt"))
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != content {
		t.Errorf("content = %q, want %q", got, content)
	}
	assertNoTempResidue(t, dest)
}

func TestGetPlainFileRedirectUsesOriginalName(t *testing.T) {
	const content = "redirected"
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		// GitHub-style redirect: the final URL path has no asset name.
		http.Redirect(w, r, "/170593871/objects", http.StatusFound)
	})
	mux.Handle("/170593871/objects", serveBytes([]byte(content)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := t.TempDir()
	if err := newTestDownloader(nil).get(context.Background(), srv.URL+"/latest", dest); err != nil {
		t.Fatalf("get: %v", err)
	}

	// The file must be named after the original request URL, not the redirect target.
	if _, err := os.Stat(filepath.Join(dest, "latest")); err != nil {
		t.Errorf("expected dest/latest to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "objects")); err == nil {
		t.Errorf("file was named after the redirect target, want the original URL basename")
	}
	assertNoTempResidue(t, dest)
}

func TestGetNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	dest := t.TempDir()
	err := newTestDownloader(nil).get(context.Background(), srv.URL+"/x.txt", dest)
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention the status code, got: %v", err)
	}
	assertNoTempResidue(t, dest)
}

func TestGetContextCanceled(t *testing.T) {
	srv := httptest.NewServer(serveBytes([]byte("data")))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dest := t.TempDir()
	if err := newTestDownloader(nil).get(ctx, srv.URL+"/x.txt", dest); err == nil {
		t.Fatal("expected an error for a canceled context")
	}
	assertNoTempResidue(t, dest)
}

type zipEntry struct {
	name    string
	content string
	mode    os.FileMode
	isDir   bool
}

// buildZip assembles an in-memory zip archive. Directory entries must use a
// trailing "/" in their name.
func buildZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.isDir {
			hdr.SetMode(os.ModeDir | 0755)
		} else {
			hdr.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", e.name, err)
		}
		if !e.isDir {
			if _, err := w.Write([]byte(e.content)); err != nil {
				t.Fatalf("write zip entry %q: %v", e.name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buf.Bytes()
}

func TestGetZipStripsTopLevel(t *testing.T) {
	zipData := buildZip(t, []zipEntry{
		{name: "topdir/", isDir: true},
		{name: "topdir/a.txt", content: "alpha", mode: 0644},
		{name: "topdir/sub/", isDir: true},
		{name: "topdir/sub/tool.exe", content: "binary", mode: 0755},
		{name: "rootfile.txt", content: "root level file", mode: 0644},
	})
	srv := httptest.NewServer(serveBytes(zipData))
	defer srv.Close()

	dest := t.TempDir()
	if err := newTestDownloader(nil).get(context.Background(), srv.URL+"/asset.zip", dest); err != nil {
		t.Fatalf("get: %v", err)
	}

	for name, want := range map[string]string{
		"a.txt":        "alpha",
		"sub/tool.exe": "binary",
		"rootfile.txt": "root level file",
	} {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("expected %s to be extracted (top-level dir should be stripped): %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", name, got, want)
		}
	}

	// Permissions are not preserved on Windows.
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dest, "sub", "tool.exe"))
		if err != nil {
			t.Fatalf("stat tool.exe: %v", err)
		}
		if fi.Mode().Perm() != 0755 {
			t.Errorf("tool.exe perms = %v, want 0755", fi.Mode().Perm())
		}
	}
	assertNoTempResidue(t, dest)
}

func TestGetZipRedirectExtracts(t *testing.T) {
	zipData := buildZip(t, []zipEntry{
		{name: "topdir/a.txt", content: "alpha", mode: 0644},
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/asset.zip", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/cdn/170593871", http.StatusFound)
	})
	mux.Handle("/cdn/170593871", serveBytes(zipData))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dest := t.TempDir()
	if err := newTestDownloader(nil).get(context.Background(), srv.URL+"/asset.zip", dest); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "a.txt")); err != nil {
		t.Errorf("expected dest/a.txt to exist: %v", err)
	}
	assertNoTempResidue(t, dest)
}

func TestGetZipRejectsPathTraversal(t *testing.T) {
	zipData := buildZip(t, []zipEntry{
		{name: "topdir/good.txt", content: "good", mode: 0644},
		// After stripping the top level this still resolves outside dest.
		{name: "topdir/../../evil.txt", content: "evil", mode: 0644},
	})
	srv := httptest.NewServer(serveBytes(zipData))
	defer srv.Close()

	dest := t.TempDir()
	err := newTestDownloader(nil).get(context.Background(), srv.URL+"/asset.zip", dest)
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected a path traversal error, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); err == nil {
		t.Errorf("evil.txt escaped the destination directory")
	}
}

type tarEntry struct {
	name    string
	content string
	mode    os.FileMode
	isDir   bool
	link    string
}

// buildTarGz assembles an in-memory tar.gz archive.
func buildTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for _, e := range entries {
		switch {
		case e.isDir:
			hdr := &tar.Header{Name: e.name, Typeflag: tar.TypeDir, Mode: int64(e.mode)}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatalf("write tar dir header %q: %v", e.name, err)
			}
		case e.link != "":
			hdr := &tar.Header{Name: e.name, Typeflag: tar.TypeSymlink, Linkname: e.link, Mode: 0777}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatalf("write tar symlink header %q: %v", e.name, err)
			}
		default:
			hdr := &tar.Header{Name: e.name, Typeflag: tar.TypeReg, Mode: int64(e.mode), Size: int64(len(e.content))}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatalf("write tar header %q: %v", e.name, err)
			}
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatalf("write tar entry %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buf.Bytes()
}

func TestGetTarGzStripsTopLevel(t *testing.T) {
	tarData := buildTarGz(t, []tarEntry{
		{name: "top-1234/", isDir: true, mode: 0755},
		{name: "top-1234/bin/", isDir: true, mode: 0755},
		{name: "top-1234/bin/tool", content: "#!/bin/sh\n", mode: 0755},
		{name: "top-1234/share/data.txt", content: "data", mode: 0644},
	})
	srv := httptest.NewServer(serveBytes(tarData))
	defer srv.Close()

	dest := t.TempDir()
	if err := newTestDownloader(nil).get(context.Background(), srv.URL+"/tool-1234.tar.gz", dest); err != nil {
		t.Fatalf("get: %v", err)
	}

	for name, want := range map[string]string{
		"bin/tool":       "#!/bin/sh\n",
		"share/data.txt": "data",
	} {
		got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("expected %s to be extracted (top-level dir should be stripped): %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", name, got, want)
		}
	}

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dest, "bin", "tool"))
		if err != nil {
			t.Fatalf("stat tool: %v", err)
		}
		if fi.Mode().Perm() != 0755 {
			t.Errorf("tool perms = %v, want 0755", fi.Mode().Perm())
		}
	}
	assertNoTempResidue(t, dest)
}

func TestGetTarGzSymlink(t *testing.T) {
	if !canSymlink(t) {
		t.Skip("symlinks not supported on this system")
	}
	tarData := buildTarGz(t, []tarEntry{
		{name: "top/", isDir: true, mode: 0755},
		{name: "top/libx.so.1", content: "so", mode: 0644},
		{name: "top/libx.so", link: "libx.so.1"},
	})
	srv := httptest.NewServer(serveBytes(tarData))
	defer srv.Close()

	dest := t.TempDir()
	if err := newTestDownloader(nil).get(context.Background(), srv.URL+"/lib.tar.gz", dest); err != nil {
		t.Fatalf("get: %v", err)
	}

	fi, err := os.Lstat(filepath.Join(dest, "libx.so"))
	if err != nil {
		t.Fatalf("lstat symlink: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("dest/libx.so is not a symlink")
	}
}

func TestGetTgzAlias(t *testing.T) {
	tarData := buildTarGz(t, []tarEntry{
		{name: "top/a.txt", content: "alpha", mode: 0644},
	})
	srv := httptest.NewServer(serveBytes(tarData))
	defer srv.Close()

	dest := t.TempDir()
	if err := newTestDownloader(nil).get(context.Background(), srv.URL+"/x.tgz", dest); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "a.txt")); err != nil {
		t.Errorf("expected dest/a.txt to exist (.tgz should be treated as tar.gz): %v", err)
	}
}

// recordingTracker records how the downloader drives a ProgressTracker.
type recordingTracker struct {
	src      string
	total    int64
	bytes    int64
	complete bool
}

func (rt *recordingTracker) TrackProgress(src string, currentSize, totalSize int64, stream io.ReadCloser) io.ReadCloser {
	rt.src = src
	rt.total = totalSize
	return &recordingReader{rt: rt, inner: stream}
}

type recordingReader struct {
	rt    *recordingTracker
	inner io.ReadCloser
}

func (rr *recordingReader) Read(p []byte) (int, error) {
	n, err := rr.inner.Read(p)
	rr.rt.bytes += int64(n)
	return n, err
}

func (rr *recordingReader) Close() error {
	rr.rt.complete = true
	return rr.inner.Close()
}

func TestProgressTrackerCalled(t *testing.T) {
	content := bytes.Repeat([]byte("libfetch"), 512) // 4 KiB
	mux := http.NewServeMux()
	mux.Handle("/known.txt", serveBytes(content))
	mux.HandleFunc("/unknown.txt", func(w http.ResponseWriter, r *http.Request) {
		// Flush mid-write to force chunked encoding, i.e. an unknown length.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content[:len(content)/2])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write(content[len(content)/2:])
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{"/known.txt", "/unknown.txt"} {
		t.Run(path, func(t *testing.T) {
			rt := &recordingTracker{}
			dest := t.TempDir()
			if err := newTestDownloader(rt).get(context.Background(), srv.URL+path, dest); err != nil {
				t.Fatalf("get: %v", err)
			}

			if rt.src != srv.URL+path {
				t.Errorf("TrackProgress src = %q, want %q", rt.src, srv.URL+path)
			}
			wantTotal := int64(len(content))
			if path == "/unknown.txt" {
				wantTotal = -1 // chunked encoding
			}
			if rt.total != wantTotal {
				t.Errorf("TrackProgress totalSize = %d, want %d", rt.total, wantTotal)
			}
			if rt.bytes != int64(len(content)) {
				t.Errorf("bytes read through tracker = %d, want %d", rt.bytes, len(content))
			}
			if !rt.complete {
				t.Errorf("tracker Close() was not called")
			}

			got, err := os.ReadFile(filepath.Join(dest, filepath.Base(path)))
			if err != nil || !bytes.Equal(got, content) {
				t.Errorf("downloaded file content mismatch (err=%v)", err)
			}
		})
	}
}
