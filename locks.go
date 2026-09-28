package libfetch

import (
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// keyedMutex provides mutual exclusion per string key. Entries are never
// removed; the set of distinct destination directories is expected to be
// small. It is process-internal only: it is NOT a cross-process lock.
type keyedMutex struct {
	mus sync.Map // string -> *sync.Mutex
}

// lock acquires the mutex for key and returns its unlock function.
func (k *keyedMutex) lock(key string) func() {
	v, _ := k.mus.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// Independent lock namespaces. InstallAsset holds an install lock for the
// whole check->download->extract->version-write sequence and then descends
// into the download layer, which takes a download lock for the same
// directory; a single shared namespace would self-deadlock because
// sync.Mutex is not reentrant. CreateVersionFile uses its own namespace for
// the same reason: the install flow calls it while already holding the
// install lock, and on Windows concurrent renames over the same target fail
// with a sharing violation unless serialized.
var (
	installLocks  keyedMutex
	downloadLocks keyedMutex
	versionLocks  keyedMutex
)

// lockKey normalizes dir into a registry key. Absolute paths make different
// spellings of one directory collide; Windows paths are lowercased because
// its filesystem is case-insensitive.
func lockKey(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}
