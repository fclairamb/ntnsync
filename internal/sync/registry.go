package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/apperrors"
	"github.com/fclairamb/ntnsync/internal/notion"
	"github.com/fclairamb/ntnsync/internal/version"
)

// maxPendingRegistryWrites bounds the in-memory write buffer. Registry writes
// are batched until the next commit so a shard is rewritten once per cycle
// instead of once per record; this is the safety valve that keeps a very large
// initial crawl from buffering everything.
const maxPendingRegistryWrites = 512

// legacyRegistryPath returns the pre-shard path of a registry file
// (.notion-sync/ids/{prefix}-{id}.json).
func legacyRegistryPath(prefix, registryID string) string {
	return idsPath(prefix + "-" + registryID + ".json")
}

// legacyPageRegistryPaths lists the historical locations of a page registry,
// most recent first:
//
//  1. page-{normalized}.json — the layout before sharding;
//  2. page-{dashed-uuid}.json — registries written before IDs were normalized on
//     every code path (notably the webhook handler). Without this fallback such
//     a page fails its file-path stability check on the next sync and gets
//     written to a second, suffixed file;
//  3. {normalized}.json — the oldest, pre-"page-" prefix format.
func legacyPageRegistryPaths(normalizedID string) []string {
	paths := []string{legacyRegistryPath("page", normalizedID)}

	if dashedID := denormalizePageID(normalizedID); dashedID != normalizedID {
		paths = append(paths, legacyRegistryPath("page", dashedID))
	}

	return append(paths, idsPath(normalizedID+".json"))
}

// loadLegacyRegistry reads a single pre-shard registry file.
func loadLegacyRegistry[T any](ctx context.Context, crawler *Crawler, path string) (*T, error) {
	data, err := crawler.store.Read(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("read registry: %w", err)
	}

	var reg T
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("unmarshal registry: %w", err)
	}

	return &reg, nil
}

// dropLegacyRegistries queues pre-shard registry files for deletion. They are
// removed by the next flush, after the shard holding the same records has been
// written, so a crash in between duplicates data rather than losing it.
func (c *Crawler) dropLegacyRegistries(paths ...string) {
	c.registryMutex.Lock()
	defer c.registryMutex.Unlock()

	for _, path := range paths {
		c.legacyDrop[path] = true
	}
}

// savePageRegistry buffers a page registry write.
//
// It canonicalizes the IDs first so the stored `id` (and `parent_id`) are always
// normalized, no matter which code path built the registry — some callers
// construct it straight from a Notion API object whose ID is in the dashed UUID
// form. This is the single choke point that guarantees the dashed/dash-less
// mismatch (which silently duplicates pages) can never be persisted.
func (c *Crawler) savePageRegistry(ctx context.Context, reg *PageRegistry) error {
	reg.ID = normalizePageID(reg.ID)
	reg.ParentID = normalizePageID(reg.ParentID)

	c.pageIndex.put(reg)
	c.dropLegacyRegistries(legacyPageRegistryPaths(reg.ID)...)

	return c.flushRegistriesIfFull(ctx)
}

// loadPageRegistry loads a page registry, looking it up by the canonical
// (normalized) ID regardless of the form the caller passes in.
//
// The shard is authoritative; the historical per-file layouts are only consulted
// for records that have not been migrated yet (see `ntnsync reindex --compact`).
func (c *Crawler) loadPageRegistry(ctx context.Context, pageID string) (*PageRegistry, error) {
	normalizedID := normalizePageID(pageID)

	reg, lookup, err := c.pageIndex.get(ctx, c, normalizedID)
	if err != nil {
		return nil, err
	}

	switch lookup {
	case shardFound:
		return reg, nil
	case shardDeleted:
		return nil, fmt.Errorf("page %s: %w", normalizedID, apperrors.ErrRegistryNotFound)
	case shardMissing:
	}

	for _, path := range legacyPageRegistryPaths(normalizedID) {
		if legacy, legacyErr := loadLegacyRegistry[PageRegistry](ctx, c, path); legacyErr == nil {
			return legacy, nil
		}
	}

	return nil, fmt.Errorf("page %s: %w", normalizedID, apperrors.ErrRegistryNotFound)
}

// deletePageRegistry removes a page from the index and drops any legacy file
// still holding it.
func (c *Crawler) deletePageRegistry(ctx context.Context, pageID string) error {
	normalizedID := normalizePageID(pageID)

	c.pageIndex.remove(normalizedID)
	c.dropLegacyRegistries(legacyPageRegistryPaths(normalizedID)...)

	return c.flushRegistriesIfFull(ctx)
}

// saveFileRegistry buffers a file registry write.
//
// SourceURL is stripped of its query string: Notion hands out pre-signed S3 URLs
// carrying ~2 KB of credentials, signature and expiry that rotate on every fetch
// and are never read back. Keeping only the bare object path preserves
// provenance without churning the record.
func (c *Crawler) saveFileRegistry(ctx context.Context, reg *FileRegistry) error {
	reg.SourceURL = stripURLQuery(reg.SourceURL)

	c.fileIndex.put(reg)
	c.dropLegacyRegistries(legacyRegistryPath("file", reg.ID))

	return c.flushRegistriesIfFull(ctx)
}

// loadFileRegistry loads a file registry by ID.
func (c *Crawler) loadFileRegistry(ctx context.Context, fileID string) (*FileRegistry, error) {
	reg, lookup, err := c.fileIndex.get(ctx, c, fileID)
	if err != nil {
		return nil, err
	}

	switch lookup {
	case shardFound:
		return reg, nil
	case shardDeleted:
		return nil, fmt.Errorf("file %s: %w", fileID, apperrors.ErrRegistryNotFound)
	case shardMissing:
	}

	legacy, legacyErr := loadLegacyRegistry[FileRegistry](ctx, c, legacyRegistryPath("file", fileID))
	if legacyErr != nil {
		return nil, fmt.Errorf("file %s: %w", fileID, apperrors.ErrRegistryNotFound)
	}

	return legacy, nil
}

// saveUserRegistry buffers a user registry write.
func (c *Crawler) saveUserRegistry(ctx context.Context, reg *UserRegistry) error {
	c.userIndex.put(reg)
	c.dropLegacyRegistries(legacyRegistryPath("user", reg.ID))

	return c.flushRegistriesIfFull(ctx)
}

// loadUserRegistry loads a user registry by ID.
func (c *Crawler) loadUserRegistry(ctx context.Context, userID string) (*UserRegistry, error) {
	reg, lookup, err := c.userIndex.get(ctx, c, userID)
	if err != nil {
		return nil, err
	}

	switch lookup {
	case shardFound:
		return reg, nil
	case shardDeleted:
		return nil, fmt.Errorf("user %s: %w", userID, apperrors.ErrRegistryNotFound)
	case shardMissing:
	}

	legacy, legacyErr := loadLegacyRegistry[UserRegistry](ctx, c, legacyRegistryPath("user", userID))
	if legacyErr != nil {
		return nil, fmt.Errorf("user %s: %w", userID, apperrors.ErrRegistryNotFound)
	}

	return legacy, nil
}

// enrichUser resolves a user's name by checking the local registry first,
// then fetching from the Notion API and caching the result.
func (c *Crawler) enrichUser(ctx context.Context, user *notion.User) {
	if user == nil || user.ID == "" || user.Name != "" {
		return
	}

	// Check file-based cache
	if reg, err := c.loadUserRegistry(ctx, user.ID); err == nil {
		user.Name = reg.Name
		user.Type = reg.Type
		if reg.Email != "" {
			user.Person = &notion.Person{Email: reg.Email}
		}
		return
	}

	// Fetch from API
	fullUser, err := c.client.GetUser(ctx, user.ID)
	if err != nil {
		c.logger.DebugContext(ctx, "failed to fetch user", "user_id", user.ID, "error", err)
		return
	}

	// Update the user in place
	user.Name = fullUser.Name
	user.Type = fullUser.Type
	user.Person = fullUser.Person
	user.Bot = fullUser.Bot

	// Save to file cache
	reg := &UserRegistry{
		NtnsyncVersion: version.Version,
		ID:             fullUser.ID,
		Name:           fullUser.Name,
		Type:           fullUser.Type,
		LastFetched:    time.Now(),
	}
	if fullUser.Person != nil {
		reg.Email = fullUser.Person.Email
	}
	if err := c.saveUserRegistry(ctx, reg); err != nil {
		c.logger.WarnContext(ctx, "failed to save user registry", "user_id", user.ID, "error", err)
	}
}

// enrichUsers enriches CreatedBy and LastEditedBy user fields.
func (c *Crawler) enrichUsers(ctx context.Context, createdBy, lastEditedBy *notion.User) {
	c.enrichUser(ctx, createdBy)
	c.enrichUser(ctx, lastEditedBy)
}

// listPageRegistries lists all page registries: the sharded index first, then
// any legacy per-page file that has not been migrated yet, de-duplicated by
// normalized ID.
func (c *Crawler) listPageRegistries(ctx context.Context) ([]*PageRegistry, error) {
	sharded, err := c.pageIndex.all(ctx, c)
	if err != nil {
		return nil, err
	}

	seen := c.pageIndex.tombstones()
	registries := make([]*PageRegistry, 0, len(sharded))

	for _, reg := range sharded {
		seen[normalizePageID(reg.ID)] = true
		registries = append(registries, reg)
	}

	return append(registries, c.listLegacyPageRegistries(ctx, seen)...), nil
}

// listLegacyPageRegistries reads the not-yet-migrated page-*.json files,
// skipping IDs already served by the sharded index.
func (c *Crawler) listLegacyPageRegistries(ctx context.Context, seen map[string]bool) []*PageRegistry {
	entries, err := c.store.List(ctx, idsPath())
	if err != nil {
		// A repository whose ids directory does not exist yet simply has no
		// legacy files to add.
		return nil
	}

	var registries []*PageRegistry

	for i := range entries {
		entry := &entries[i]
		if entry.IsDir || !strings.HasSuffix(entry.Path, ".json") {
			continue
		}
		if !strings.HasPrefix(filepath.Base(entry.Path), "page-") {
			continue
		}

		reg, loadErr := loadLegacyRegistry[PageRegistry](ctx, c, entry.Path)
		if loadErr != nil {
			continue
		}

		normalizedID := normalizePageID(reg.ID)
		if seen[normalizedID] {
			continue
		}
		seen[normalizedID] = true

		registries = append(registries, reg)
	}

	return registries
}

// hasPendingRegistryWork reports whether anything is waiting to be written.
func (c *Crawler) hasPendingRegistryWork() bool {
	c.registryMutex.Lock()
	legacy := len(c.legacyDrop)
	c.registryMutex.Unlock()

	return legacy > 0 ||
		c.pageIndex.dirtyCount() > 0 ||
		c.fileIndex.dirtyCount() > 0 ||
		c.userIndex.dirtyCount() > 0
}

// pendingRegistryWrites counts the buffered record writes and deletions.
func (c *Crawler) pendingRegistryWrites() int {
	return c.pageIndex.dirtyCount() + c.fileIndex.dirtyCount() + c.userIndex.dirtyCount()
}

// flushRegistriesIfFull flushes early when the write buffer grows past
// maxPendingRegistryWrites, bounding memory on very large crawls.
func (c *Crawler) flushRegistriesIfFull(ctx context.Context) error {
	if c.pendingRegistryWrites() < maxPendingRegistryWrites {
		return nil
	}

	return c.FlushRegistries(ctx)
}

// FlushRegistries writes every buffered registry record to disk, one write per
// affected shard, and then removes the legacy per-record files those shards
// replaced. It is a no-op when nothing is pending.
func (c *Crawler) FlushRegistries(ctx context.Context) error {
	if !c.hasPendingRegistryWork() {
		return nil
	}

	if err := c.EnsureTransaction(ctx); err != nil {
		return fmt.Errorf("ensure transaction: %w", err)
	}

	written := 0

	for _, flush := range []func(context.Context, *Crawler) (int, error){
		c.pageIndex.flush,
		c.fileIndex.flush,
		c.userIndex.flush,
	} {
		count, err := flush(ctx, c)
		written += count

		if err != nil {
			return fmt.Errorf("flush registry index: %w", err)
		}
	}

	if written > 0 {
		if err := c.writeIDsManifest(ctx); err != nil {
			return err
		}
	}

	if err := c.deleteLegacyRegistries(ctx); err != nil {
		return err
	}

	c.logger.DebugContext(ctx, "flushed registry index", "shards_written", written)

	return nil
}

// invalidateRegistryCache drops every cached shard, so the next read comes from
// disk. See shardIndex.invalidate.
func (c *Crawler) invalidateRegistryCache() {
	c.pageIndex.invalidate()
	c.fileIndex.invalidate()
	c.userIndex.invalidate()
}

// writeIDsManifest writes .notion-sync/ids/manifest.json, which holds the
// ntnsync and schema versions once for the whole index instead of repeating
// them in every record. Written at most once per crawler, and only when the
// content would actually change.
func (c *Crawler) writeIDsManifest(ctx context.Context) error {
	c.registryMutex.Lock()
	defer c.registryMutex.Unlock()

	if c.manifestSynced {
		return nil
	}

	manifest := idsManifest{
		NtnsyncVersion: version.Version,
		SchemaVersion:  idsSchemaVersion,
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ids manifest: %w", err)
	}

	data = append(data, '\n')
	path := idsPath(idsManifestFile)

	if existing, readErr := c.store.Read(ctx, path); readErr == nil && bytes.Equal(existing, data) {
		c.manifestSynced = true
		return nil
	}

	if err := c.tx.Write(ctx, path, data); err != nil {
		return fmt.Errorf("write ids manifest: %w", err)
	}

	c.manifestSynced = true

	return nil
}

// deleteLegacyRegistries removes the pre-shard registry files queued for
// deletion. Called after the shards have been written.
func (c *Crawler) deleteLegacyRegistries(ctx context.Context) error {
	c.registryMutex.Lock()
	paths := slices.Sorted(maps.Keys(c.legacyDrop))
	c.legacyDrop = make(map[string]bool)
	c.registryMutex.Unlock()

	for _, path := range paths {
		exists, err := c.store.Exists(ctx, path)
		if err != nil || !exists {
			continue
		}

		if delErr := c.tx.Delete(ctx, path); delErr != nil {
			return fmt.Errorf("delete legacy registry %s: %w", path, delErr)
		}
	}

	return nil
}
