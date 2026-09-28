package libfetch

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
)

func TestDefaultProgressTrackerConcurrentUse(t *testing.T) {
	tracker := DefaultProgressTracker()

	const workers = 16
	const payloadSize = 16 * 1024

	// Each goroutine downloads a distinct payload through the SAME tracker;
	// with the old shared-reader implementation the readers overwrote each
	// other's state, cross-contaminated data, and closed the wrong stream.
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := bytes.Repeat([]byte{byte('a' + w)}, payloadSize)
			wrapped := tracker.TrackProgress("payload", 0, int64(payloadSize), io.NopCloser(bytes.NewReader(payload)))
			if wrapped == nil {
				errs[w] = io.ErrUnexpectedEOF
				return
			}
			got, err := io.ReadAll(wrapped)
			if err != nil {
				errs[w] = err
				return
			}
			if !bytes.Equal(got, payload) {
				errs[w] = errors.New("data cross-contaminated between readers")
				return
			}
			if err := wrapped.Close(); err != nil {
				errs[w] = err
			}
		}()
	}
	wg.Wait()

	for w, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", w, err)
		}
	}
}

func TestTrackProgressCompleteWhenSizesEqual(t *testing.T) {
	tracker := DefaultProgressTracker()
	rc := io.NopCloser(bytes.NewReader(nil))
	if got := tracker.TrackProgress("src", 5, 5, rc); got != nil {
		t.Errorf("TrackProgress(complete) = %v, want nil", got)
	}
}
