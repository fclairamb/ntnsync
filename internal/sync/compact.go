package sync

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// normalizedIDLength is the length of a dash-less Notion UUID.
const normalizedIDLength = 32

// legacy registry filename prefixes, from the layouts that predate sharding.
const (
	legacyPagePrefix = "page-"
	legacyFilePrefix = "file-"
	legacyUserPrefix = "user-"
)

// compactStats counts what a compaction migrated.
type compactStats struct {
	pages int
	files int
	users int
}

// isBareIDRegistryName reports whether name is the oldest registry filename
// format: a normalized page ID with no prefix, e.g. 2c536f5e…95d.json.
func isBareIDRegistryName(name string) bool {
	stem := strings.TrimSuffix(name, ".json")
	if len(stem) != normalizedIDLength {
		return false
	}

	for i := range len(stem) {
		if !isHexDigit(stem[i]) {
			return false
		}
	}

	return true
}

// Absorption ranks: when several legacy files describe the same entity, the one
// with the highest rank is absorbed last and therefore wins.
const (
	rankBareID         = iota // {id}.json, the oldest format
	rankDashedPage            // page-{dashed-uuid}.json
	rankNormalizedPage        // page-{normalized}.json, the canonical form
	rankOtherRegistry         // file-/user- registries, no duplicates to arbitrate
)

// legacyRegistryRank returns the absorption rank of a legacy registry filename.
func legacyRegistryRank(name string) int {
	switch {
	case isBareIDRegistryName(name):
		return rankBareID
	case strings.HasPrefix(name, legacyPagePrefix):
		if len(strings.TrimSuffix(strings.TrimPrefix(name, legacyPagePrefix), ".json")) != normalizedIDLength {
			return rankDashedPage
		}
		return rankNormalizedPage
	default:
		return rankOtherRegistry
	}
}

// Compact migrates the legacy one-file-per-record registry layout
// (.notion-sync/ids/page-{id}.json and friends) into the sharded JSON Lines
// index, then deletes the old files — all in a single commit.
//
// Readers running an older binary keep working until that commit lands, because
// loadPageRegistry still probes the legacy locations.
func (c *Crawler) Compact(ctx context.Context, dryRun bool) error {
	c.logger.InfoContext(ctx, "compacting registry index", "dry_run", dryRun)

	paths, err := c.legacyRegistryPaths(ctx)
	if err != nil {
		return err
	}

	stats := &compactStats{}
	migrated := make([]string, 0, len(paths))

	for _, path := range paths {
		if c.absorbLegacyRegistry(ctx, path, stats) {
			migrated = append(migrated, path)
		}
	}

	c.logger.InfoContext(ctx, "compact summary",
		"pages", stats.pages,
		"files", stats.files,
		"users", stats.users,
		"legacy_files", len(migrated))

	if dryRun {
		c.logger.InfoContext(ctx, "dry run - no changes made")
		return nil
	}

	if len(migrated) == 0 {
		c.logger.InfoContext(ctx, "registry index already compact")
		return nil
	}

	if err := c.EnsureTransaction(ctx); err != nil {
		return fmt.Errorf("ensure transaction: %w", err)
	}

	// Queued after the shards are written by FlushRegistries, so a crash in
	// between duplicates records rather than losing them.
	c.dropLegacyRegistries(migrated...)

	if err := c.FlushRegistries(ctx); err != nil {
		return fmt.Errorf("flush registries: %w", err)
	}

	if err := c.CommitChanges(ctx, "[ntnsync] reindex --compact: sharded registry index"); err != nil {
		return err
	}

	c.logger.InfoContext(ctx, "compact complete")

	return nil
}

// legacyRegistryPaths lists the pre-shard registry files, ordered so that the
// most canonical duplicate of an entity is absorbed last.
func (c *Crawler) legacyRegistryPaths(ctx context.Context) ([]string, error) {
	entries, err := c.store.List(ctx, idsPath())
	if err != nil {
		return nil, fmt.Errorf("list ids directory: %w", err)
	}

	paths := make([]string, 0, len(entries))

	for i := range entries {
		entry := &entries[i]
		if entry.IsDir || !strings.HasSuffix(entry.Path, ".json") {
			continue
		}
		if filepath.Base(entry.Path) == idsManifestFile {
			continue
		}
		paths = append(paths, entry.Path)
	}

	slices.SortStableFunc(paths, func(left, right string) int {
		leftName, rightName := filepath.Base(left), filepath.Base(right)
		if order := cmp.Compare(legacyRegistryRank(leftName), legacyRegistryRank(rightName)); order != 0 {
			return order
		}
		return cmp.Compare(leftName, rightName)
	})

	return paths, nil
}

// absorbLegacyRegistry reads one legacy registry file into the sharded index.
// It reports whether the file was recognized and can therefore be deleted.
func (c *Crawler) absorbLegacyRegistry(ctx context.Context, path string, stats *compactStats) bool {
	name := filepath.Base(path)

	switch {
	case strings.HasPrefix(name, legacyPagePrefix) || isBareIDRegistryName(name):
		reg, err := loadLegacyRegistry[PageRegistry](ctx, c, path)
		if err != nil || reg.ID == "" {
			c.logger.WarnContext(ctx, "skipping unreadable page registry", "path", path, "error", err)
			return false
		}
		reg.ID = normalizePageID(reg.ID)
		reg.ParentID = normalizePageID(reg.ParentID)
		c.pageIndex.put(reg)
		stats.pages++

	case strings.HasPrefix(name, legacyFilePrefix):
		reg, err := loadLegacyRegistry[FileRegistry](ctx, c, path)
		if err != nil || reg.ID == "" {
			c.logger.WarnContext(ctx, "skipping unreadable file registry", "path", path, "error", err)
			return false
		}
		reg.SourceURL = stripURLQuery(reg.SourceURL)
		c.fileIndex.put(reg)
		stats.files++

	case strings.HasPrefix(name, legacyUserPrefix):
		reg, err := loadLegacyRegistry[UserRegistry](ctx, c, path)
		if err != nil || reg.ID == "" {
			c.logger.WarnContext(ctx, "skipping unreadable user registry", "path", path, "error", err)
			return false
		}
		c.userIndex.put(reg)
		stats.users++

	default:
		return false
	}

	return true
}
