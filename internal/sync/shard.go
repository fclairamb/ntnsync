package sync

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	gosync "sync"
)

const (
	// shardCount is the number of shards a sharded index is split into. Notion
	// IDs are UUIDs, so the first two hex characters distribute uniformly and
	// 256 shards keep each one small enough (~123 records) that rewriting it is
	// cheap after git delta compression, while the directory tree stays trivial.
	shardCount = 256

	// shardKeyLength is the number of hex characters used as the shard key.
	shardKeyLength = 2

	// shardCacheSize is how many parsed shards are kept in memory at once.
	shardCacheSize = 32

	// idsSchemaVersion is the on-disk schema version of the sharded ids index.
	idsSchemaVersion = 1

	// pageShardDir/fileShardDir are the sub-directories of .notion-sync/ids
	// holding the page and file shards.
	pageShardDir = "page"
	fileShardDir = "file"

	// userShardName is the single (unsharded) user index: ~119 records is not
	// worth splitting.
	userShardName = "user"

	// shardExt is the extension of a shard file.
	shardExt = ".jsonl"

	// idsManifestFile holds the ntnsync/schema version once for the whole index,
	// instead of repeating it inside every record.
	idsManifestFile = "manifest.json"

	// ntnsyncVersionKey is the record field moved to the manifest.
	ntnsyncVersionKey = "ntnsync_version"
)

// idsManifest is stored at .notion-sync/ids/manifest.json. It carries the
// information that used to be duplicated in every single registry record.
type idsManifest struct {
	NtnsyncVersion string `json:"ntnsync_version"`
	SchemaVersion  int    `json:"schema_version"`
}

// idsPath builds a path below .notion-sync/ids.
func idsPath(elem ...string) string {
	return filepath.Join(append([]string{stateDir, idsDir}, elem...)...)
}

// isHexDigit reports whether chr is a lowercase hexadecimal digit.
func isHexDigit(chr byte) bool {
	return (chr >= '0' && chr <= '9') || (chr >= 'a' && chr <= 'f')
}

// shardKeyFor derives the shard key of an entity ID: the first two hex
// characters of its normalized (dash-less, lower-cased) form.
//
// Notion IDs are UUIDs so this distributes uniformly without hashing. IDs that
// are too short, or that do not start with two hex characters, fall back to an
// FNV-1a bucket so that a valid two-character key is always produced.
func shardKeyFor(rawID string) string {
	normalized := strings.ToLower(strings.ReplaceAll(rawID, "-", ""))

	if len(normalized) >= shardKeyLength && isHexDigit(normalized[0]) && isHexDigit(normalized[1]) {
		return normalized[:shardKeyLength]
	}

	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(normalized))

	return hex.EncodeToString([]byte{byte(hasher.Sum32() % shardCount)})
}

// shardPathFunc returns a path builder for the shards of sub-directory sub.
func shardPathFunc(sub string) func(key string) string {
	return func(key string) string {
		return idsPath(sub, key+shardExt)
	}
}

// canonicalRecordJSON marshals a registry record as compact JSON with sorted
// keys. Determinism is the whole point of the sharded layout: a one-record
// change must produce a one-line diff so git can delta-compress the shard.
//
// The per-record ntnsync_version is dropped — it lives in manifest.json.
func canonicalRecordJSON(rec any) ([]byte, error) {
	raw, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("marshal record: %w", err)
	}

	fields := make(map[string]json.RawMessage)
	if unmarshalErr := json.Unmarshal(raw, &fields); unmarshalErr != nil {
		return nil, fmt.Errorf("normalize record: %w", unmarshalErr)
	}

	delete(fields, ntnsyncVersionKey)

	// encoding/json emits map keys in sorted order.
	canonical, marshalErr := json.Marshal(fields)
	if marshalErr != nil {
		return nil, fmt.Errorf("marshal canonical record: %w", marshalErr)
	}

	return canonical, nil
}

// cloneRecord returns a shallow copy so callers cannot mutate cached records.
func cloneRecord[T any](rec *T) *T {
	clone := *rec
	return &clone
}

// shardIndex is a read-through cache and write buffer over a set of JSON Lines
// shard files.
//
// Reads parse at most shardCacheSize shards and keep them in an LRU. Writes are
// buffered in dirty and only hit the filesystem on flush, so a sync touching 50
// pages rewrites each affected shard exactly once per commit cycle.
type shardIndex[T any] struct {
	mutex   gosync.Mutex
	keyFor  func(rawID string) string
	pathFor func(key string) string
	dirName string // listed to enumerate shards; empty for single-file indexes
	idOf    func(rec *T) string

	cache map[string]map[string]*T
	order []string                 // LRU order, least-recently-used first
	dirty map[string]map[string]*T // shard key -> id -> record (nil = tombstone)
}

// newShardIndex builds an index over the shards of sub-directory sub.
func newShardIndex[T any](sub string, idOf func(rec *T) string) *shardIndex[T] {
	return &shardIndex[T]{
		keyFor:  shardKeyFor,
		pathFor: shardPathFunc(sub),
		dirName: idsPath(sub),
		idOf:    idOf,
		cache:   make(map[string]map[string]*T),
		dirty:   make(map[string]map[string]*T),
	}
}

// newSingleShardIndex builds an index stored in one file, for record sets too
// small to be worth sharding.
func newSingleShardIndex[T any](name string, idOf func(rec *T) string) *shardIndex[T] {
	path := idsPath(name + shardExt)

	return &shardIndex[T]{
		keyFor:  func(string) string { return "" },
		pathFor: func(string) string { return path },
		idOf:    idOf,
		cache:   make(map[string]map[string]*T),
		dirty:   make(map[string]map[string]*T),
	}
}

// parseShard decodes a JSON Lines shard into records keyed by ID.
func (idx *shardIndex[T]) parseShard(path string, data []byte) (map[string]*T, error) {
	records := make(map[string]*T)

	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		var rec T
		if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
			return nil, fmt.Errorf("parse shard %s: %w", path, err)
		}

		records[idx.idOf(&rec)] = &rec
	}

	return records, nil
}

// readShard returns the parsed contents of a shard, loading it from the store
// on a cache miss. A shard that does not exist yet reads as empty.
func (idx *shardIndex[T]) readShard(ctx context.Context, crawler *Crawler, key string) (map[string]*T, error) {
	if cached, ok := idx.cache[key]; ok {
		idx.touch(key)
		return cached, nil
	}

	path := idx.pathFor(key)

	exists, err := crawler.store.Exists(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("stat shard %s: %w", path, err)
	}

	records := make(map[string]*T)

	if exists {
		data, readErr := crawler.store.Read(ctx, path)
		if readErr != nil {
			return nil, fmt.Errorf("read shard %s: %w", path, readErr)
		}

		records, err = idx.parseShard(path, data)
		if err != nil {
			return nil, err
		}
	}

	idx.cache[key] = records
	idx.touch(key)
	idx.evict()

	return records, nil
}

// touch marks a shard as most recently used.
func (idx *shardIndex[T]) touch(key string) {
	if pos := slices.Index(idx.order, key); pos >= 0 {
		idx.order = slices.Delete(idx.order, pos, pos+1)
	}
	idx.order = append(idx.order, key)
}

// evict drops the least recently used shards above the cache budget. Dirty
// records live outside the cache, so eviction never loses a pending write.
func (idx *shardIndex[T]) evict() {
	for len(idx.order) > shardCacheSize {
		delete(idx.cache, idx.order[0])
		idx.order = idx.order[1:]
	}
}

// shardLookup is the outcome of a shard lookup.
type shardLookup int

const (
	// shardMissing means the index knows nothing about the ID; the caller may
	// fall back to the legacy per-record files.
	shardMissing shardLookup = iota
	// shardFound means the record was returned.
	shardFound
	// shardDeleted means the record was explicitly deleted. The legacy fallback
	// must NOT run, otherwise a deletion pending flush would be resurrected.
	shardDeleted
)

// get returns the record for recID, looking at pending writes first.
func (idx *shardIndex[T]) get(ctx context.Context, crawler *Crawler, recID string) (*T, shardLookup, error) {
	idx.mutex.Lock()
	defer idx.mutex.Unlock()

	key := idx.keyFor(recID)

	if pending, ok := idx.dirty[key]; ok {
		if rec, found := pending[recID]; found {
			if rec == nil {
				return nil, shardDeleted, nil
			}
			return cloneRecord(rec), shardFound, nil
		}
	}

	records, err := idx.readShard(ctx, crawler, key)
	if err != nil {
		return nil, shardMissing, err
	}

	rec, ok := records[recID]
	if !ok {
		return nil, shardMissing, nil
	}

	return cloneRecord(rec), shardFound, nil
}

// tombstones returns the IDs deleted but not yet flushed, so a caller scanning
// legacy files does not resurrect them.
func (idx *shardIndex[T]) tombstones() map[string]bool {
	idx.mutex.Lock()
	defer idx.mutex.Unlock()

	deleted := make(map[string]bool)

	for _, bucket := range idx.dirty {
		for recID, rec := range bucket {
			if rec == nil {
				deleted[recID] = true
			}
		}
	}

	return deleted
}

// pending returns (creating it if needed) the dirty bucket of a shard.
func (idx *shardIndex[T]) pending(key string) map[string]*T {
	bucket, ok := idx.dirty[key]
	if !ok {
		bucket = make(map[string]*T)
		idx.dirty[key] = bucket
	}
	return bucket
}

// put buffers a record. Nothing is written until flush.
func (idx *shardIndex[T]) put(rec *T) {
	idx.mutex.Lock()
	defer idx.mutex.Unlock()

	recID := idx.idOf(rec)
	idx.pending(idx.keyFor(recID))[recID] = cloneRecord(rec)
}

// remove buffers a deletion. Nothing is written until flush.
func (idx *shardIndex[T]) remove(recID string) {
	idx.mutex.Lock()
	defer idx.mutex.Unlock()

	idx.pending(idx.keyFor(recID))[recID] = nil
}

// dirtyCount returns the number of buffered writes and deletions.
func (idx *shardIndex[T]) dirtyCount() int {
	idx.mutex.Lock()
	defer idx.mutex.Unlock()

	total := 0
	for _, bucket := range idx.dirty {
		total += len(bucket)
	}
	return total
}

// shardKeys lists every shard key that currently has content, on disk or
// pending, in a stable order.
func (idx *shardIndex[T]) shardKeys(ctx context.Context, crawler *Crawler) []string {
	seen := make(map[string]bool)

	if idx.dirName == "" {
		seen[""] = true
	} else if entries, err := crawler.store.List(ctx, idx.dirName); err == nil {
		for i := range entries {
			entry := &entries[i]
			if entry.IsDir || !strings.HasSuffix(entry.Path, shardExt) {
				continue
			}
			seen[strings.TrimSuffix(filepath.Base(entry.Path), shardExt)] = true
		}
	}

	for key := range idx.dirty {
		seen[key] = true
	}

	return slices.Sorted(maps.Keys(seen))
}

// merged returns the effective contents of a shard: what is on disk with the
// pending writes and deletions applied on top.
func (idx *shardIndex[T]) merged(ctx context.Context, crawler *Crawler, key string) (map[string]*T, error) {
	records, err := idx.readShard(ctx, crawler, key)
	if err != nil {
		return nil, err
	}

	pending, hasPending := idx.dirty[key]
	if !hasPending {
		return records, nil
	}

	effective := make(map[string]*T, len(records)+len(pending))
	maps.Copy(effective, records)

	for recID, rec := range pending {
		if rec == nil {
			delete(effective, recID)
			continue
		}
		effective[recID] = rec
	}

	return effective, nil
}

// all returns every record held by the index, pending writes included.
func (idx *shardIndex[T]) all(ctx context.Context, crawler *Crawler) ([]*T, error) {
	idx.mutex.Lock()
	defer idx.mutex.Unlock()

	keys := idx.shardKeys(ctx, crawler)

	var out []*T

	for _, key := range keys {
		effective, err := idx.merged(ctx, crawler, key)
		if err != nil {
			return nil, err
		}

		for _, recID := range slices.Sorted(maps.Keys(effective)) {
			out = append(out, cloneRecord(effective[recID]))
		}
	}

	return out, nil
}

// writeShard writes one shard as sorted, compact JSON Lines, or deletes it when
// it has become empty.
func (idx *shardIndex[T]) writeShard(
	ctx context.Context, crawler *Crawler, key string, records map[string]*T,
) error {
	path := idx.pathFor(key)

	if len(records) == 0 {
		if err := crawler.tx.Delete(ctx, path); err != nil {
			return fmt.Errorf("delete empty shard %s: %w", path, err)
		}
		return nil
	}

	var buf bytes.Buffer

	for _, recID := range slices.Sorted(maps.Keys(records)) {
		line, err := canonicalRecordJSON(records[recID])
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	if err := crawler.tx.Write(ctx, path, buf.Bytes()); err != nil {
		return fmt.Errorf("write shard %s: %w", path, err)
	}

	return nil
}

// flush writes every dirty shard exactly once and returns how many were
// written. A shard touched by fifty records is still a single write.
func (idx *shardIndex[T]) flush(ctx context.Context, crawler *Crawler) (int, error) {
	idx.mutex.Lock()
	defer idx.mutex.Unlock()

	if len(idx.dirty) == 0 {
		return 0, nil
	}

	written := 0

	for _, key := range slices.Sorted(maps.Keys(idx.dirty)) {
		effective, err := idx.merged(ctx, crawler, key)
		if err != nil {
			return written, err
		}

		if err := idx.writeShard(ctx, crawler, key, effective); err != nil {
			return written, err
		}

		idx.cache[key] = effective
		idx.touch(key)
		idx.evict()
		written++
	}

	idx.dirty = make(map[string]map[string]*T)

	return written, nil
}
