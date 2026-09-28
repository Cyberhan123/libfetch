package libfetch

import (
	"fmt"
	"io"
	"time"
)

// ProgressTracker tracks the progress of a download.
//
// It has the same method set as getter.ProgressTracker from
// github.com/hashicorp/go-getter, which this library used before becoming
// dependency-free, so existing tracker implementations keep working.
type ProgressTracker interface {
	TrackProgress(src string, currentSize, totalSize int64, stream io.ReadCloser) io.ReadCloser
}

// DefaultProgressTracker returns the default ProgressTracker that prints download progress to stdout.
//
// The returned tracker is safe for concurrent use: every TrackProgress call
// hands out an independent reader, so concurrent downloads never share state.
func DefaultProgressTracker() ProgressTracker {
	progFunc := func(src string, currentSize int64, totalSize int64, mibPerSec float64, complete bool) {
		fmt.Printf("\r\x1b[Kdownloading %s... %d MiB of %d MiB (%.2f MiB/s)", src, currentSize/(1024*1024), totalSize/(1024*1024), mibPerSec)
		if complete {
			fmt.Println()
		}
	}

	return &defaultTracker{progress: progFunc}
}

var _ ProgressTracker = (*defaultTracker)(nil)

// defaultTracker hands out an independent progressReader per download, so a
// single tracker can safely track concurrent downloads.
type defaultTracker struct {
	progress progressFunc
}

func (t *defaultTracker) TrackProgress(src string, currentSize, totalSize int64, stream io.ReadCloser) io.ReadCloser {
	if currentSize == totalSize {
		return nil
	}

	return &progressReader{
		src:         src,
		currentSize: currentSize,
		totalSize:   totalSize,
		startTime:   time.Now(),
		reader:      stream,
		progress:    t.progress,
	}
}

type progressFunc func(src string, currentSize int64, totalSize int64, mibPerSec float64, complete bool)

type progressReader struct {
	src          string
	currentSize  int64
	totalSize    int64
	lastReported int64
	startTime    time.Time
	reader       io.ReadCloser
	progress     progressFunc
}

const (
	mib    = 1024 * 1024
	mib100 = mib * 100
)

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.currentSize += int64(n)

	if pr.progress != nil && pr.currentSize-pr.lastReported >= mib100 {
		pr.lastReported = pr.currentSize
		pr.progress(pr.src, pr.currentSize, pr.totalSize, pr.mibPerSec(), false)
	}

	return n, err
}

func (pr *progressReader) Close() error {
	if pr.progress != nil {
		pr.progress(pr.src, pr.currentSize, pr.totalSize, pr.mibPerSec(), true)
	}

	return pr.reader.Close()
}

func (pr *progressReader) mibPerSec() float64 {
	elapsed := time.Since(pr.startTime).Seconds()
	if elapsed == 0 {
		return 0
	}

	return float64(pr.currentSize) / mib / elapsed
}
