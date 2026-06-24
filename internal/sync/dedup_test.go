package sync

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/fclairamb/ntnsync/internal/notion"
	"github.com/fclairamb/ntnsync/internal/store"
)

// These tests reproduce the duplicate-page bug where the same Notion page was
// written to two files, e.g.
//
//	.../cristal/comite-strategique.md          (first sync)
//	.../cristal/comite-strategique-388a.md     (later sync, spurious suffix)
//
// Root cause: a page whose registry was stored under the dashed UUID form
// (page-388aa28b-3ffb-...json, e.g. from a Notion webhook event) failed both its
// file-path stability check and the conflict resolver's self-comparison, so it
// was treated as a new, colliding page and given a "-{shortID}" suffix.

const (
	dashedID     = "388aa28b-3ffb-80b6-9e5b-c6a0eeaebf64"
	normalizedID = "388aa28b3ffb80b69e5bc6a0eeaebf64"
	cristalDir   = "csm/csm-accompagnement-client/rdv-clients-plateforme/cristal"
)

// writeLegacyDashedRegistry writes a page registry under the legacy dashed ID
// filename, mimicking what the webhook path used to produce.
func writeLegacyDashedRegistry(t *testing.T, tmpDir, filePath string) {
	t.Helper()
	regPath := filepath.Join(tmpDir, ".notion-sync/ids", "page-"+dashedID+".json")
	content := `{"id":"` + dashedID + `","type":"page","folder":"csm",` +
		`"file_path":"` + filePath + `","title":"062026 Comite strategique",` +
		`"last_edited":"2026-06-23T13:28:00Z","last_synced":"2026-06-23T13:34:15Z",` +
		`"is_root":false,"parent_id":"159aa28b3ffb808aa6dbfdb9fa28c1d9"}`
	if err := os.WriteFile(regPath, []byte(content), 0600); err != nil {
		t.Fatalf("write legacy registry: %v", err)
	}
}

func newDedupTestCrawler(t *testing.T) (*Crawler, string) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "sync_dedup")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	if mkErr := os.MkdirAll(filepath.Join(tmpDir, ".notion-sync/ids"), 0750); mkErr != nil {
		t.Fatalf("mkdir: %v", mkErr)
	}

	st, err := store.NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return NewCrawler(nil, st, WithCrawlerLogger(slog.Default())), tmpDir
}

// TestLoadPageRegistry_FindsLegacyDashedFile verifies a registry written under
// the dashed ID is found when looked up by the normalized ID. This is what keeps
// the file-path stability check working for legacy pages.
func TestLoadPageRegistry_FindsLegacyDashedFile(t *testing.T) {
	t.Parallel()
	crawler, tmpDir := newDedupTestCrawler(t)
	wantPath := cristalDir + "/comite-strategique.md"
	writeLegacyDashedRegistry(t, tmpDir, wantPath)

	reg, err := crawler.loadPageRegistry(context.Background(), normalizedID)
	if err != nil {
		t.Fatalf("loadPageRegistry by normalized id should find the dashed file: %v", err)
	}
	if reg.FilePath != wantPath {
		t.Errorf("FilePath = %q, want %q", reg.FilePath, wantPath)
	}
}

// TestResolveFilenameConflict_SkipsSelfWithDashedRegistry verifies a page does
// not collide with its own legacy (dashed-ID) registry and therefore is NOT
// given a spurious suffix.
func TestResolveFilenameConflict_SkipsSelfWithDashedRegistry(t *testing.T) {
	t.Parallel()
	crawler, tmpDir := newDedupTestCrawler(t)
	writeLegacyDashedRegistry(t, tmpDir, cristalDir+"/comite-strategique.md")

	got := crawler.resolveFilenameConflict(
		context.Background(), "csm", cristalDir, "comite-strategique", normalizedID)

	if got != "comite-strategique" {
		t.Errorf("resolveFilenameConflict() = %q, want %q (no suffix: the only registry IS this page)",
			got, "comite-strategique")
	}
}

// TestSavePageRegistry_NormalizesDashedID verifies the write choke point: a
// registry built from a dashed (Notion API form) ID is persisted under the
// normalized filename and `id`, never as page-{uuid-with-dashes}.json.
func TestSavePageRegistry_NormalizesDashedID(t *testing.T) {
	t.Parallel()
	crawler, tmpDir := newDedupTestCrawler(t)
	ctx := context.Background()
	tx, err := crawler.store.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	crawler.SetTransaction(tx)

	reg := &PageRegistry{
		ID:       dashedID,
		ParentID: "159aa28b-3ffb-808a-a6db-fdb9fa28c1d9",
		Type:     "page",
		FilePath: cristalDir + "/comite-strategique.md",
	}
	if saveErr := crawler.savePageRegistry(ctx, reg); saveErr != nil {
		t.Fatalf("savePageRegistry: %v", saveErr)
	}

	idsDirPath := filepath.Join(tmpDir, ".notion-sync/ids")
	if _, statErr := os.Stat(filepath.Join(idsDirPath, "page-"+normalizedID+".json")); statErr != nil {
		t.Errorf("expected normalized registry file to exist: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(idsDirPath, "page-"+dashedID+".json")); !os.IsNotExist(statErr) {
		t.Errorf("dashed registry file must not be written (stat err = %v)", statErr)
	}
	if reg.ID != normalizedID {
		t.Errorf("reg.ID = %q, want normalized %q", reg.ID, normalizedID)
	}
	if reg.ParentID != "159aa28b3ffb808aa6dbfdb9fa28c1d9" {
		t.Errorf("reg.ParentID = %q, want normalized", reg.ParentID)
	}
}

// TestComputeFilePath_StableForLegacyDashedRegistry is the end-to-end assertion:
// re-syncing a page that was registered under the dashed ID must reuse its
// existing path instead of producing a second, suffixed file.
func TestComputeFilePath_StableForLegacyDashedRegistry(t *testing.T) {
	t.Parallel()
	crawler, tmpDir := newDedupTestCrawler(t)
	wantPath := cristalDir + "/comite-strategique.md"
	writeLegacyDashedRegistry(t, tmpDir, wantPath)

	// Notion hands us the dashed form on re-sync.
	page := &notion.Page{ID: dashedID}
	got := crawler.computeFilePath(context.Background(), page, "csm", false, "159aa28b3ffb808aa6dbfdb9fa28c1d9")

	if got != wantPath {
		t.Errorf("computeFilePath() = %q, want %q (must be stable, no -388a duplicate)", got, wantPath)
	}
}
