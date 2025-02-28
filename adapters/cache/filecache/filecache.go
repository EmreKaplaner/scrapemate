package filecache

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/EmreKaplaner/scrapemate"
	"github.com/EmreKaplaner/scrapemate/adapters/cache"
)

var _ scrapemate.Cacher = (*FileCache)(nil)

// FileCache is a file cache
type FileCache struct {
	folder string
	mutex  sync.RWMutex // Add mutex for concurrent operations
}

// NewFileCache creates a new file cache
func NewFileCache(folder string) (*FileCache, error) {
	const permissions = 0o777
	if err := os.MkdirAll(folder, permissions); err != nil {
		return nil, fmt.Errorf("cannot create cache dir %w", err)
	}

	return &FileCache{folder: folder}, nil
}

// Get gets a value from the cache
func (c *FileCache) Get(_ context.Context, key string) (scrapemate.Response, error) {
	c.mutex.RLock() // Use read lock for concurrent reads
	defer c.mutex.RUnlock()

	file := filepath.Join(c.folder, key)

	f, err := os.Open(file)
	if err != nil {
		return scrapemate.Response{}, fmt.Errorf("cannot open file %s: %w", file, err)
	}

	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return scrapemate.Response{}, fmt.Errorf("cannot read file %s: %w", file, err)
	}

	decompressed, err := cache.Decompress(data)
	if err != nil {
		return scrapemate.Response{}, fmt.Errorf("cannot decompress file %s: %w", file, err)
	}

	var response scrapemate.Response

	if err := json.Unmarshal(decompressed, &response); err != nil {
		return scrapemate.Response{}, fmt.Errorf("cannot unmarshal file %s: %w", file, err)
	}

	return response, nil
}

// Set sets a value to the cache
func (c *FileCache) Set(_ context.Context, key string, value *scrapemate.Response) error {
	c.mutex.Lock() // Use write lock for exclusive write access
	defer c.mutex.Unlock()

	// Create a temp file first to avoid corruption during write
	tempFile := filepath.Join(c.folder, fmt.Sprintf("%s.tmp", key))
	targetFile := filepath.Join(c.folder, key)

	f, err := os.Create(tempFile)
	if err != nil {
		return fmt.Errorf("cannot create temp file: %w", err)
	}

	defer f.Close()

	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cannot marshal response %w", err)
	}

	compressed, err := cache.Compress(data)
	if err != nil {
		return fmt.Errorf("cannot compress data %w", err)
	}

	if _, err := f.Write(compressed); err != nil {
		return fmt.Errorf("cannot write to file %w", err)
	}

	// Make sure the data is on disk
	if err := f.Sync(); err != nil {
		return fmt.Errorf("cannot sync file to disk: %w", err)
	}

	// Close before rename
	if err := f.Close(); err != nil {
		return fmt.Errorf("cannot close file: %w", err)
	}

	// Atomic rename to ensure consistency
	if err := os.Rename(tempFile, targetFile); err != nil {
		return fmt.Errorf("cannot finalize cache file: %w", err)
	}

	return nil
}

// Close closes the file cache
func (c *FileCache) Close() error {
	return nil
}
