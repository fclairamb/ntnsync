package webhook

import (
	"context"
	"testing"
)

// TestHandlePageChange_NormalizesEntityID verifies that a webhook event carrying
// Notion's dashed UUID is queued under the canonical (dash-less) ID. Queuing the
// dashed form is what caused the same page to later be written to a second,
// suffixed file (e.g. comite-strategique-388a.md).
func TestHandlePageChange_NormalizesEntityID(t *testing.T) {
	t.Parallel()
	handler := createTestHandlerWithoutSecret(t)
	ctx := context.Background()

	tx, err := handler.store.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	handler.queueManager.SetTransaction(tx)

	const dashedID = "388aa28b-3ffb-80b6-9e5b-c6a0eeaebf64"
	const normalizedID = "388aa28b3ffb80b69e5bc6a0eeaebf64"

	event := &Event{
		Type:   "page.updated",
		Entity: &Entity{ID: dashedID, Type: "page"},
	}
	handler.handlePageChange(ctx, event, tx)

	files, err := handler.queueManager.ListEntries(ctx)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 queue entry, got %d", len(files))
	}

	entry, err := handler.queueManager.ReadEntry(ctx, files[0])
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if len(entry.Pages) != 1 {
		t.Fatalf("expected 1 queued page, got %d", len(entry.Pages))
	}
	if entry.Pages[0].ID != normalizedID {
		t.Errorf("queued page ID = %q, want normalized %q", entry.Pages[0].ID, normalizedID)
	}
}
