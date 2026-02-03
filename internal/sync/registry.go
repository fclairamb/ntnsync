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
func saveRegistry[T any](c *Crawler, ctx context.Context, prefix, id string, data *T) error {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}

	path := filepath.Join(stateDir, idsDir, fmt.Sprintf("%s-%s.json", prefix, id))
	if err := c.tx.Write(ctx, path, jsonData); err != nil {
		return fmt.Errorf("write registry: %w", err)
	}

	return nil
}

// loadRegistry loads a registry file with the given prefix and ID.
func loadRegistry[T any](c *Crawler, ctx context.Context, prefix, id string) (*T, error) {
	path := filepath.Join(stateDir, idsDir, fmt.Sprintf("%s-%s.json", prefix, id))
	data, err := c.store.Read(ctx, path)
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
func (c *Crawler) savePageRegistry(ctx context.Context, reg *PageRegistry) error {
	return saveRegistry(c, ctx, "page", reg.ID, reg)
}

// loadPageRegistry loads a page registry file.
// Tries page-{id}.json format first, falls back to old format ({id}.json) for backward compatibility.
func (c *Crawler) loadPageRegistry(ctx context.Context, pageID string) (*PageRegistry, error) {
	reg, err := loadRegistry[PageRegistry](c, ctx, "page", pageID)
	if err != nil {
		// Fall back to old format for backward compatibility
		oldPath := filepath.Join(stateDir, idsDir, pageID+".json")
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

	return reg, nil
}

// saveFileRegistry saves a file registry to disk.
func (c *Crawler) saveFileRegistry(ctx context.Context, reg *FileRegistry) error {
	return saveRegistry(c, ctx, "file", reg.ID, reg)
}

// loadFileRegistry loads a file registry by ID.
func (c *Crawler) loadFileRegistry(ctx context.Context, fileID string) (*FileRegistry, error) {
	return loadRegistry[FileRegistry](c, ctx, "file", fileID)
}

// saveUserRegistry saves a user registry file.
func (c *Crawler) saveUserRegistry(ctx context.Context, reg *UserRegistry) error {
	return saveRegistry(c, ctx, "user", reg.ID, reg)
}

// loadUserRegistry loads a user registry file.
func (c *Crawler) loadUserRegistry(ctx context.Context, userID string) (*UserRegistry, error) {
	return loadRegistry[UserRegistry](c, ctx, "user", userID)
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
