package testfiles

import (
	"container/list"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// DiskCache stores test files on local disk, named by their SHA-256, and
// evicts the least recently used files when the total size exceeds the
// limit. Files in use by a running job are pinned and never evicted.
type DiskCache struct {
	dir      string
	maxBytes int64

	mu         sync.Mutex
	entries    map[string]*list.Element
	lru        *list.List // front = most recently used
	totalBytes int64
}

type cacheEntry struct {
	sha256 string
	size   int64
	pins   int
}

// OpenDiskCache indexes files already on disk (so the cache survives
// restarts) and removes leftover partial downloads.
func OpenDiskCache(dir string, maxBytes int64) (*DiskCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	cache := &DiskCache{dir: dir, maxBytes: maxBytes, entries: map[string]*list.Element{}, lru: list.New()}
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type existing struct {
		name string
		info os.FileInfo
	}
	var found []existing
	for _, file := range files {
		path := filepath.Join(dir, file.Name())
		if strings.HasSuffix(file.Name(), ".partial") {
			_ = os.Remove(path)
			continue
		}
		if !isSHA256(file.Name()) {
			continue
		}
		info, err := file.Info()
		if err != nil {
			continue
		}
		found = append(found, existing{file.Name(), info})
	}
	// Oldest modification time is least recently used.
	sort.Slice(found, func(left, right int) bool { return found[left].info.ModTime().Before(found[right].info.ModTime()) })
	for _, file := range found {
		cache.entries[file.name] = cache.lru.PushFront(&cacheEntry{sha256: file.name, size: file.info.Size()})
		cache.totalBytes += file.info.Size()
	}
	cache.mu.Lock()
	cache.evictLocked()
	cache.mu.Unlock()
	return cache, nil
}

func (c *DiskCache) path(sha256 string) string { return filepath.Join(c.dir, sha256) }

// partialPath is where a download is written before it is verified.
func (c *DiskCache) partialPath(sha256 string) string {
	return filepath.Join(c.dir, sha256+".partial")
}

// Pin returns the path of a cached file and pins it until release is
// called. ok is false when the file is not cached.
func (c *DiskCache) Pin(sha256 string) (path string, release func(), ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, found := c.entries[sha256]
	if !found {
		return "", nil, false
	}
	entry := element.Value.(*cacheEntry)
	entry.pins++
	c.lru.MoveToFront(element)
	return c.path(sha256), c.releaser(entry), true
}

// Commit moves a verified partial download into the cache and pins it.
func (c *DiskCache) Commit(sha256 string, size int64) (path string, release func(), err error) {
	if err := os.Rename(c.partialPath(sha256), c.path(sha256)); err != nil {
		return "", nil, fmt.Errorf("commit cached file: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := &cacheEntry{sha256: sha256, size: size, pins: 1}
	if element, found := c.entries[sha256]; found {
		entry = element.Value.(*cacheEntry)
		entry.pins++
		c.lru.MoveToFront(element)
	} else {
		c.entries[sha256] = c.lru.PushFront(entry)
		c.totalBytes += size
	}
	c.evictLocked()
	return c.path(sha256), c.releaser(entry), nil
}

func (c *DiskCache) releaser(entry *cacheEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			entry.pins--
			c.evictLocked()
		})
	}
}

// evictLocked removes unpinned files from the back of the LRU list until
// the cache fits. Pinned files may keep it over the limit temporarily.
func (c *DiskCache) evictLocked() {
	element := c.lru.Back()
	for c.totalBytes > c.maxBytes && element != nil {
		previous := element.Prev()
		entry := element.Value.(*cacheEntry)
		if entry.pins == 0 {
			_ = os.Remove(c.path(entry.sha256))
			c.lru.Remove(element)
			delete(c.entries, entry.sha256)
			c.totalBytes -= entry.size
		}
		element = previous
	}
}

// TotalBytes is the size of all cached files.
func (c *DiskCache) TotalBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalBytes
}

func isSHA256(name string) bool {
	if len(name) != 64 {
		return false
	}
	for _, character := range name {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}
