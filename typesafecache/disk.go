package typesafecache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	typesafe "github.com/nibir1/typesafe-go"
)

// DefaultDiskMaxBytes bounds the disk tier when a size is not given.
//
// A cache that grows without limit is a disk-filler with a friendly name. 256
// MiB holds a large number of responses — they are small — while staying far
// below anything that would surprise an operator.
const DefaultDiskMaxBytes int64 = 256 << 20

// WithDisk adds an on-disk tier under dir, which is created if missing.
//
// The memory tier is checked first; a disk hit is promoted into memory, since
// a hit that stays on disk pays the decode cost on every lookup. The disk tier
// survives process restarts, which is the point — a batch job that reruns
// after a crash should not pay for the work it already did.
//
// Entries respect the same TTL as memory. The size bound is enforced on write
// by deleting the oldest files first.
func WithDisk(dir string) Option {
	return WithDiskLimit(dir, DefaultDiskMaxBytes)
}

// WithDiskLimit is WithDisk with an explicit size bound in bytes. Zero means
// DefaultDiskMaxBytes.
func WithDiskLimit(dir string, maxBytes int64) Option {
	return func(c *Cache) error {
		if dir == "" {
			return fmt.Errorf("typesafecache: disk directory must not be empty")
		}
		if maxBytes < 0 {
			return fmt.Errorf("typesafecache: disk limit must not be negative")
		}
		if maxBytes == 0 {
			maxBytes = DefaultDiskMaxBytes
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("typesafecache: creating %s: %w", dir, err)
		}
		c.disk = &diskTier{dir: dir, maxBytes: maxBytes}
		return nil
	}
}

// diskTier is a flat directory of one JSON file per key.
//
// Flat and one-file-per-key on purpose: the keys are SHA-256 hex, so they are
// already uniformly distributed and fixed-length, an individual entry can be
// deleted without rewriting anything else, and a corrupt file costs exactly
// one entry. A single index file would be faster to scan and would lose the
// whole cache to one bad write.
type diskTier struct {
	dir      string
	maxBytes int64
}

// diskEntry is the on-disk form. The stored-at timestamp travels with the
// entry so TTL survives a restart; file mtime would be close enough until
// someone copies the directory.
type diskEntry struct {
	StoredAt time.Time                   `json:"stored_at"`
	Response *typesafe.SystemOneResponse `json:"response"`
}

func (d *diskTier) path(key string) string {
	// Keys are hex SHA-256 from canonical.Hash, so they are safe as file
	// names. Anything else never reaches here.
	return filepath.Join(d.dir, key+".json")
}

// get reads an entry, returning (nil, nil) for a miss.
//
// A file that cannot be decoded is deleted and reported as a miss: a corrupt
// entry that keeps failing to parse would otherwise produce an error on every
// lookup of that key, forever.
func (d *diskTier) get(key string, now time.Time, ttl time.Duration) (*typesafe.SystemOneResponse, error) {
	b, err := os.ReadFile(d.path(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("typesafecache: reading %s: %w", key, err)
	}

	var e diskEntry
	if err := json.Unmarshal(b, &e); err != nil {
		_ = os.Remove(d.path(key))
		return nil, fmt.Errorf("typesafecache: %s was corrupt and has been removed: %w", key, err)
	}
	if e.Response == nil || now.Sub(e.StoredAt) > ttl {
		_ = os.Remove(d.path(key))
		return nil, nil
	}
	return e.Response, nil
}

// put writes an entry and enforces the size bound.
//
// The write goes to a temp file and is renamed into place, so a crash halfway
// through leaves the previous entry intact rather than a truncated one that
// the next read would have to delete.
func (d *diskTier) put(key string, resp *typesafe.SystemOneResponse, now time.Time) error {
	b, err := json.Marshal(diskEntry{StoredAt: now, Response: resp})
	if err != nil {
		return fmt.Errorf("typesafecache: encoding %s: %w", key, err)
	}

	tmp, err := os.CreateTemp(d.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("typesafecache: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("typesafecache: writing %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("typesafecache: closing %s: %w", key, err)
	}
	if err := os.Rename(tmpName, d.path(key)); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("typesafecache: installing %s: %w", key, err)
	}

	return d.prune()
}

// prune deletes the oldest entries until the directory fits the bound.
func (d *diskTier) prune() error {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return fmt.Errorf("typesafecache: scanning %s: %w", d.dir, err)
	}

	type fileInfo struct {
		name string
		mod  time.Time
		size int64
	}
	files := make([]fileInfo, 0, len(entries))
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // vanished under us; nothing to prune
		}
		files = append(files, fileInfo{e.Name(), info.ModTime(), info.Size()})
		total += info.Size()
	}
	if total <= d.maxBytes {
		return nil
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= d.maxBytes {
			break
		}
		if err := os.Remove(filepath.Join(d.dir, f.name)); err != nil {
			continue
		}
		total -= f.size
	}
	return nil
}

// purge removes every entry.
func (d *diskTier) purge() error {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return fmt.Errorf("typesafecache: scanning %s: %w", d.dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(d.dir, e.Name())); err != nil {
			return fmt.Errorf("typesafecache: removing %s: %w", e.Name(), err)
		}
	}
	return nil
}
