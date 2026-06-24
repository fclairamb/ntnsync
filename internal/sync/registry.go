package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/notion"
	"github.com/fclairamb/ntnsync/internal/version"
)

// saveRegistry saves a registry file with the given prefix and ID.
func saveRegistry[T any](ctx context.Context, crawler *Crawler, prefix, registryID string, data *T) error {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}

	path := filepath.Join(stateDir, idsDir, fmt.Sprintf("%s-%s.json", prefix, registryID))
	if err := crawler.tx.Write(ctx, path, jsonData); err != nil {
		return fmt.Errorf("write registry: %w", err)
	}

	return nil
}

// loadRegistry loads a registry file with the given prefix and ID.
func loadRegistry[T any](ctx context.Context, crawler *Crawler, prefix, registryID string) (*T, error) {
	path := filepath.Join(stateDir, idsDir, fmt.Sprintf("%s-%s.json", prefix, registryID))
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

// savePageRegistry saves a page registry file.
//
// It canonicalizes the IDs first so the registry filename and the stored `id`
// (and `parent_id`) are always normalized, no matter which code path built the
// registry — some callers construct it straight from a Notion API object whose
// ID is in the dashed UUID form. This is the single choke point that guarantees
// the dashed/dash-less mismatch (which silently duplicates pages) can never be
// persisted.
func (c *Crawler) savePageRegistry(ctx context.Context, reg *PageRegistry) error {
	reg.ID = normalizePageID(reg.ID)
	reg.ParentID = normalizePageID(reg.ParentID)
	return saveRegistry(ctx, c, "page", reg.ID, reg)
}

// loadPageRegistry loads a page registry file, looking it up by the canonical
// (normalized) ID regardless of the form the caller passes in.
//
// It tries, in order:
//  1. page-{normalized}.json — the canonical location;
//  2. page-{dashed-uuid}.json — legacy registries written before IDs were
//     normalized on every code path (notably the webhook handler). Without this
//     fallback such a page fails its file-path stability check on the next sync
//     and gets written to a second, suffixed file;
//  3. {normalized}.json — the oldest pre-"page-" prefix format.
func (c *Crawler) loadPageRegistry(ctx context.Context, pageID string) (*PageRegistry, error) {
	normalizedID := normalizePageID(pageID)

	if reg, err := loadRegistry[PageRegistry](ctx, c, "page", normalizedID); err == nil {
		return reg, nil
	}

	// Legacy dashed form, e.g. page-388aa28b-3ffb-80b6-9e5b-c6a0eeaebf64.json.
	if dashedID := denormalizePageID(normalizedID); dashedID != normalizedID {
		if reg, err := loadRegistry[PageRegistry](ctx, c, "page", dashedID); err == nil {
			return reg, nil
		}
	}

	// Oldest format ({id}.json, no "page-" prefix) for backward compatibility.
	oldPath := filepath.Join(stateDir, idsDir, normalizedID+".json")
	data, readErr := c.store.Read(ctx, oldPath)
	if readErr != nil {
		return nil, fmt.Errorf("read registry: %w", readErr)
	}

	var oldReg PageRegistry
	if unmarshalErr := json.Unmarshal(data, &oldReg); unmarshalErr != nil {
		return nil, fmt.Errorf("unmarshal registry: %w", unmarshalErr)
	}

	return &oldReg, nil
}

// saveFileRegistry saves a file registry to disk.
func (c *Crawler) saveFileRegistry(ctx context.Context, reg *FileRegistry) error {
	return saveRegistry(ctx, c, "file", reg.ID, reg)
}

// loadFileRegistry loads a file registry by ID.
func (c *Crawler) loadFileRegistry(ctx context.Context, fileID string) (*FileRegistry, error) {
	return loadRegistry[FileRegistry](ctx, c, "file", fileID)
}

// saveUserRegistry saves a user registry file.
func (c *Crawler) saveUserRegistry(ctx context.Context, reg *UserRegistry) error {
	return saveRegistry(ctx, c, "user", reg.ID, reg)
}

// loadUserRegistry loads a user registry file.
func (c *Crawler) loadUserRegistry(ctx context.Context, userID string) (*UserRegistry, error) {
	return loadRegistry[UserRegistry](ctx, c, "user", userID)
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

// listPageRegistries lists all page registries.
func (c *Crawler) listPageRegistries(ctx context.Context) ([]*PageRegistry, error) {
	idsPath := filepath.Join(stateDir, idsDir)
	entries, err := c.store.List(ctx, idsPath)
	if err != nil {
		return nil, err
	}

	var registries []*PageRegistry
	for i := range entries {
		entry := &entries[i]
		// Skip directories and non-page registry files
		if entry.IsDir || !strings.HasSuffix(entry.Path, ".json") {
			continue
		}
		// Only include page- prefixed files (skip file- registries)
		if !strings.HasPrefix(filepath.Base(entry.Path), "page-") {
			continue
		}

		data, err := c.store.Read(ctx, entry.Path)
		if err != nil {
			continue
		}

		var reg PageRegistry
		if err := json.Unmarshal(data, &reg); err != nil {
			continue
		}

		registries = append(registries, &reg)
	}

	return registries, nil
}
