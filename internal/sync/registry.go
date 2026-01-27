package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// savePageRegistry saves a page registry file.
func (c *Crawler) savePageRegistry(_ context.Context, reg *PageRegistry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}

	path := filepath.Join(stateDir, idsDir, fmt.Sprintf("page-%s.json", reg.ID))
	if err := c.tx.Write(path, data); err != nil {
		return fmt.Errorf("write registry: %w", err)
	}

	return nil
}

// loadPageRegistry loads a page registry file.
// Tries page-{id}.json format first, falls back to old format ({id}.json) for backward compatibility.
func (c *Crawler) loadPageRegistry(ctx context.Context, pageID string) (*PageRegistry, error) {
	// Try page- prefix format first
	path := filepath.Join(stateDir, idsDir, fmt.Sprintf("page-%s.json", pageID))
	data, err := c.store.Read(ctx, path)
	if err != nil {
		// Fall back to old format for backward compatibility
		oldPath := filepath.Join(stateDir, idsDir, pageID+".json")
		data, err = c.store.Read(ctx, oldPath)
		if err != nil {
			return nil, fmt.Errorf("read registry: %w", err)
		}
	}

	var reg PageRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("unmarshal registry: %w", err)
	}

	return &reg, nil
}

// saveFileRegistry saves a file registry to disk.
func (c *Crawler) saveFileRegistry(_ context.Context, reg *FileRegistry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal file registry: %w", err)
	}

	path := filepath.Join(stateDir, idsDir, fmt.Sprintf("file-%s.json", reg.ID))
	if err := c.tx.Write(path, data); err != nil {
		return fmt.Errorf("write file registry: %w", err)
	}

	return nil
}

// loadFileRegistry loads a file registry by ID.
func (c *Crawler) loadFileRegistry(ctx context.Context, fileID string) (*FileRegistry, error) {
	path := filepath.Join(stateDir, idsDir, fmt.Sprintf("file-%s.json", fileID))
	data, err := c.store.Read(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("read file registry: %w", err)
	}

	var reg FileRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("unmarshal file registry: %w", err)
	}

	return &reg, nil
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
