package sync

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fclairamb/ntnsync/internal/store"
)

// newShardTestCrawler builds a crawler backed by a fresh git-backed store in a
// temporary directory, with a transaction already attached.
func newShardTestCrawler(t *testing.T) (*Crawler, *store.LocalStore, string) {
	t.Helper()

	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".notion-sync/ids"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	st, err := store.NewLocalStore(tmpDir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	crawler := NewCrawler(nil, st, WithCrawlerLogger(slog.Default()))
	if txErr := crawler.EnsureTransaction(context.Background()); txErr != nil {
		t.Fatalf("begin tx: %v", txErr)
	}

	return crawler, st, tmpDir
}

// writeLegacyFile writes a raw file below .notion-sync/ids.
func writeLegacyFile(t *testing.T, tmpDir, name, content string) {
	t.Helper()

	path := filepath.Join(tmpDir, ".notion-sync", "ids", name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// readShardFile returns the raw bytes of a page shard.
func readShardFile(t *testing.T, tmpDir, sub, key string) string {
	t.Helper()

	path := filepath.Join(tmpDir, ".notion-sync", "ids", sub, key+".jsonl")
	data, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("read shard %s: %v", path, err)
	}

	return string(data)
}

func TestShardKeyFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		rawID string
		want  string
	}{
		{"normalized uuid", "388aa28b3ffb80b69e5bc6a0eeaebf64", "38"},
		{"dashed uuid", "388aa28b-3ffb-80b6-9e5b-c6a0eeaebf64", "38"},
		{"uppercase", "AB8AA28B3FFB80B69E5BC6A0EEAEBF64", "ab"},
		{"leading zeroes", "00112233445566778899aabbccddeeff", "00"},
		{"trailing shard", "ff112233445566778899aabbccddeeff", "ff"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := shardKeyFor(testCase.rawID); got != testCase.want {
				t.Errorf("shardKeyFor(%q) = %q, want %q", testCase.rawID, got, testCase.want)
			}
		})
	}
}

// TestShardKeyFor_NonHexFallback checks that IDs that are not UUID-shaped still
// land in a valid, stable shard instead of producing a bogus filename.
func TestShardKeyFor_NonHexFallback(t *testing.T) {
	t.Parallel()

	for _, rawID := range []string{"", "z", "zz", "not-a-uuid", "  "} {
		key := shardKeyFor(rawID)

		if len(key) != shardKeyLength {
			t.Errorf("shardKeyFor(%q) = %q, want %d characters", rawID, key, shardKeyLength)
		}
		for i := range len(key) {
			if !isHexDigit(key[i]) {
				t.Errorf("shardKeyFor(%q) = %q, want hex characters", rawID, key)
			}
		}
		if again := shardKeyFor(rawID); again != key {
			t.Errorf("shardKeyFor(%q) is not stable: %q then %q", rawID, key, again)
		}
	}
}

// TestPageRegistryShardRoundTrip saves records, flushes them and reads them back
// through a *different* crawler, so the values genuinely come off disk.
func TestPageRegistryShardRoundTrip(t *testing.T) {
	t.Parallel()

	crawler, st, _ := newShardTestCrawler(t)
	ctx := context.Background()
	edited := time.Date(2026, 6, 23, 13, 28, 0, 0, time.UTC)

	reg := &PageRegistry{
		ID:         dashedID,
		Type:       notionTypePage,
		Folder:     "csm",
		FilePath:   cristalDir + "/comite-strategique.md",
		Title:      "Comite strategique",
		LastEdited: edited,
		Children:   []string{"aa112233445566778899aabbccddeeff"},
	}
	if err := crawler.savePageRegistry(ctx, reg); err != nil {
		t.Fatalf("savePageRegistry: %v", err)
	}
	if err := crawler.FlushRegistries(ctx); err != nil {
		t.Fatalf("FlushRegistries: %v", err)
	}

	reader := NewCrawler(nil, st, WithCrawlerLogger(slog.Default()))

	loaded, err := reader.loadPageRegistry(ctx, dashedID)
	if err != nil {
		t.Fatalf("loadPageRegistry: %v", err)
	}

	if loaded.ID != normalizedID {
		t.Errorf("ID = %q, want %q", loaded.ID, normalizedID)
	}
	if loaded.FilePath != reg.FilePath {
		t.Errorf("FilePath = %q, want %q", loaded.FilePath, reg.FilePath)
	}
	if loaded.Title != reg.Title {
		t.Errorf("Title = %q, want %q", loaded.Title, reg.Title)
	}
	if !loaded.LastEdited.Equal(edited) {
		t.Errorf("LastEdited = %v, want %v", loaded.LastEdited, edited)
	}
	if len(loaded.Children) != 1 || loaded.Children[0] != reg.Children[0] {
		t.Errorf("Children = %v, want %v", loaded.Children, reg.Children)
	}
}

// TestShardOutputIsDeterministic is the property the whole layout depends on:
// identical content must produce identical bytes, records must be sorted by id,
// keys must be sorted inside each record, and ntnsync_version must not be
// repeated per record.
func TestShardOutputIsDeterministic(t *testing.T) {
	t.Parallel()

	crawler, _, tmpDir := newShardTestCrawler(t)
	ctx := context.Background()

	// Same shard ("aa"), deliberately saved out of order.
	ids := []string{
		"aa33333333333333333333333333cccc",
		"aa11111111111111111111111111aaaa",
		"aa22222222222222222222222222bbbb",
	}
	for _, pageID := range ids {
		if err := crawler.savePageRegistry(ctx, &PageRegistry{
			NtnsyncVersion: "v1.2.3",
			ID:             pageID,
			Type:           notionTypePage,
			Folder:         "tech",
			FilePath:       "tech/" + pageID + ".md",
			Title:          "Page " + pageID,
		}); err != nil {
			t.Fatalf("savePageRegistry: %v", err)
		}
	}
	if err := crawler.FlushRegistries(ctx); err != nil {
		t.Fatalf("FlushRegistries: %v", err)
	}

	content := readShardFile(t, tmpDir, pageShardDir, "aa")
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

	if len(lines) != len(ids) {
		t.Fatalf("shard has %d lines, want %d: %s", len(lines), len(ids), content)
	}
	if strings.Contains(content, ntnsyncVersionKey) {
		t.Errorf("shard must not repeat %s per record: %s", ntnsyncVersionKey, content)
	}

	seenIDs := make([]string, 0, len(lines))
	for _, line := range lines {
		keys := recordKeys(t, line)
		if !slices.IsSorted(keys) {
			t.Errorf("record keys are not sorted: %v", keys)
		}

		var rec PageRegistry
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		seenIDs = append(seenIDs, rec.ID)
	}

	if !slices.IsSorted(seenIDs) {
		t.Errorf("records are not sorted by id: %v", seenIDs)
	}

	// The same records saved in a different order, in a different repository,
	// must produce byte-identical output.
	other, _, otherDir := newShardTestCrawler(t)

	for _, pageID := range slices.Backward(ids) {
		if err := other.savePageRegistry(ctx, &PageRegistry{
			NtnsyncVersion: "v9.9.9",
			ID:             pageID,
			Type:           notionTypePage,
			Folder:         "tech",
			FilePath:       "tech/" + pageID + ".md",
			Title:          "Page " + pageID,
		}); err != nil {
			t.Fatalf("savePageRegistry: %v", err)
		}
	}
	if err := other.FlushRegistries(ctx); err != nil {
		t.Fatalf("FlushRegistries: %v", err)
	}

	if again := readShardFile(t, otherDir, pageShardDir, "aa"); again != content {
		t.Errorf("shard output is not deterministic:\n%s\nvs\n%s", content, again)
	}
}

// recordKeys returns the object keys of a JSON record, in file order.
func recordKeys(t *testing.T, line string) []string {
	t.Helper()

	decoder := json.NewDecoder(strings.NewReader(line))

	if _, err := decoder.Token(); err != nil { // opening brace
		t.Fatalf("decode line: %v", err)
	}

	var keys []string

	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatalf("decode key: %v", err)
		}

		key, ok := token.(string)
		if !ok {
			t.Fatalf("unexpected key token %v", token)
		}
		keys = append(keys, key)

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("decode value: %v", err)
		}
	}

	return keys
}

// countingTx wraps a transaction and counts writes per path, so a test can
// prove a shard is written once per flush rather than once per record.
type countingTx struct {
	inner  store.Transaction
	writes map[string]int
}

func (t *countingTx) Write(ctx context.Context, path string, content []byte) error {
	t.writes[path]++
	return t.inner.Write(ctx, path, content)
}

func (t *countingTx) WriteStream(ctx context.Context, path string, reader io.Reader) (int64, error) {
	t.writes[path]++
	return t.inner.WriteStream(ctx, path, reader)
}

func (t *countingTx) Delete(ctx context.Context, path string) error {
	return t.inner.Delete(ctx, path)
}

func (t *countingTx) Mkdir(ctx context.Context, path string) error {
	return t.inner.Mkdir(ctx, path)
}

func (t *countingTx) Commit(ctx context.Context, message string) error {
	return t.inner.Commit(ctx, message)
}

func (t *countingTx) Rollback(ctx context.Context) error {
	return t.inner.Rollback(ctx)
}

// TestFlushWritesEachShardOnce checks the batching requirement: many records
// landing in the same shard produce a single write.
func TestFlushWritesEachShardOnce(t *testing.T) {
	t.Parallel()

	crawler, st, _ := newShardTestCrawler(t)
	ctx := context.Background()

	inner, err := st.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	counter := &countingTx{inner: inner, writes: make(map[string]int)}
	crawler.SetTransaction(counter)

	for _, pageID := range []string{
		"aa11111111111111111111111111aaaa",
		"aa22222222222222222222222222bbbb",
		"aa33333333333333333333333333cccc",
		"aa44444444444444444444444444dddd",
		"bb55555555555555555555555555eeee",
	} {
		if saveErr := crawler.savePageRegistry(ctx, &PageRegistry{
			ID:       pageID,
			Type:     notionTypePage,
			Folder:   "tech",
			FilePath: "tech/" + pageID + ".md",
		}); saveErr != nil {
			t.Fatalf("savePageRegistry: %v", saveErr)
		}
	}

	if flushErr := crawler.FlushRegistries(ctx); flushErr != nil {
		t.Fatalf("FlushRegistries: %v", flushErr)
	}

	shardAA := idsPath(pageShardDir, "aa"+shardExt)
	shardBB := idsPath(pageShardDir, "bb"+shardExt)

	if counter.writes[shardAA] != 1 {
		t.Errorf("shard aa written %d times, want 1 (4 records must be batched)", counter.writes[shardAA])
	}
	if counter.writes[shardBB] != 1 {
		t.Errorf("shard bb written %d times, want 1", counter.writes[shardBB])
	}
}

// TestLoadRegistriesFallBackToLegacyFiles checks that a repository that has not
// been compacted yet keeps resolving every historical layout.
func TestLoadRegistriesFallBackToLegacyFiles(t *testing.T) {
	t.Parallel()

	crawler, _, tmpDir := newShardTestCrawler(t)
	ctx := context.Background()

	oldestID := "cc112233445566778899aabbccddeeff"
	writeLegacyFile(t, tmpDir, oldestID+".json",
		`{"id":"`+oldestID+`","type":"page","folder":"tech","file_path":"tech/oldest.md"}`)
	writeLegacyFile(t, tmpDir, "file-abc123.json",
		`{"id":"abc123","file_path":"tech/files/a.png","source_url":"https://s3/a.png"}`)
	writeLegacyFile(t, tmpDir, "user-u1.json",
		`{"id":"u1","name":"Alice","type":"person"}`)

	pageReg, err := crawler.loadPageRegistry(ctx, oldestID)
	if err != nil {
		t.Fatalf("loadPageRegistry (oldest format): %v", err)
	}
	if pageReg.FilePath != "tech/oldest.md" {
		t.Errorf("FilePath = %q, want tech/oldest.md", pageReg.FilePath)
	}

	fileReg, err := crawler.loadFileRegistry(ctx, "abc123")
	if err != nil {
		t.Fatalf("loadFileRegistry (legacy): %v", err)
	}
	if fileReg.FilePath != "tech/files/a.png" {
		t.Errorf("FilePath = %q, want tech/files/a.png", fileReg.FilePath)
	}

	userReg, err := crawler.loadUserRegistry(ctx, "u1")
	if err != nil {
		t.Fatalf("loadUserRegistry (legacy): %v", err)
	}
	if userReg.Name != "Alice" {
		t.Errorf("Name = %q, want Alice", userReg.Name)
	}
}

// TestListPageRegistriesMixesShardsAndLegacyFiles covers a half-migrated
// repository: both sources must be visible, with the shard winning on overlap.
func TestListPageRegistriesMixesShardsAndLegacyFiles(t *testing.T) {
	t.Parallel()

	crawler, _, tmpDir := newShardTestCrawler(t)
	ctx := context.Background()

	shardedID := "aa11111111111111111111111111aaaa"
	legacyOnlyID := "bb22222222222222222222222222bbbb"

	if err := crawler.savePageRegistry(ctx, &PageRegistry{
		ID: shardedID, Type: notionTypePage, Folder: "tech", FilePath: "tech/sharded.md",
	}); err != nil {
		t.Fatalf("savePageRegistry: %v", err)
	}
	if err := crawler.FlushRegistries(ctx); err != nil {
		t.Fatalf("FlushRegistries: %v", err)
	}

	writeLegacyFile(t, tmpDir, "page-"+legacyOnlyID+".json",
		`{"id":"`+legacyOnlyID+`","type":"page","folder":"tech","file_path":"tech/legacy.md"}`)
	// Same page as the shard, stale copy: must not be listed twice.
	writeLegacyFile(t, tmpDir, "page-"+shardedID+".json",
		`{"id":"`+shardedID+`","type":"page","folder":"tech","file_path":"tech/stale.md"}`)

	registries, err := crawler.listPageRegistries(ctx)
	if err != nil {
		t.Fatalf("listPageRegistries: %v", err)
	}

	paths := make(map[string]string, len(registries))
	for _, reg := range registries {
		if _, dup := paths[reg.ID]; dup {
			t.Errorf("page %s listed twice", reg.ID)
		}
		paths[reg.ID] = reg.FilePath
	}

	if len(registries) != 2 {
		t.Fatalf("got %d registries, want 2: %v", len(registries), paths)
	}
	if paths[shardedID] != "tech/sharded.md" {
		t.Errorf("sharded page = %q, want tech/sharded.md (the shard wins)", paths[shardedID])
	}
	if paths[legacyOnlyID] != "tech/legacy.md" {
		t.Errorf("legacy page = %q, want tech/legacy.md", paths[legacyOnlyID])
	}
}

// TestDeletePageRegistryRemovesShardAndLegacy checks a deletion is not undone by
// the legacy fallback.
func TestDeletePageRegistryRemovesShardAndLegacy(t *testing.T) {
	t.Parallel()

	crawler, _, tmpDir := newShardTestCrawler(t)
	ctx := context.Background()

	pageID := "aa11111111111111111111111111aaaa"
	writeLegacyFile(t, tmpDir, "page-"+pageID+".json",
		`{"id":"`+pageID+`","type":"page","folder":"tech","file_path":"tech/gone.md"}`)

	if err := crawler.savePageRegistry(ctx, &PageRegistry{
		ID: pageID, Type: notionTypePage, Folder: "tech", FilePath: "tech/gone.md",
	}); err != nil {
		t.Fatalf("savePageRegistry: %v", err)
	}
	if err := crawler.FlushRegistries(ctx); err != nil {
		t.Fatalf("FlushRegistries: %v", err)
	}

	if err := crawler.deletePageRegistry(ctx, pageID); err != nil {
		t.Fatalf("deletePageRegistry: %v", err)
	}
	if _, err := crawler.loadPageRegistry(ctx, pageID); err == nil {
		t.Error("page must not resolve after deletion (pending flush)")
	}

	if err := crawler.FlushRegistries(ctx); err != nil {
		t.Fatalf("FlushRegistries: %v", err)
	}
	if _, err := crawler.loadPageRegistry(ctx, pageID); err == nil {
		t.Error("page must not resolve after deletion (flushed)")
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".notion-sync/ids", "page-"+pageID+".json")); !os.IsNotExist(err) {
		t.Errorf("legacy file must be gone, stat err = %v", err)
	}
	// The only record is gone, so the shard file itself must not linger.
	if _, err := os.Stat(filepath.Join(tmpDir, ".notion-sync/ids", pageShardDir, "aa.jsonl")); !os.IsNotExist(err) {
		t.Errorf("empty shard must be removed, stat err = %v", err)
	}
}

func TestStripURLQuery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		rawURL string
		want   string
	}{
		{"https://s3/notion/a.png?X-Amz-Signature=deadbeef&X-Amz-Expires=3600", "https://s3/notion/a.png"},
		{"https://s3/notion/a.png", "https://s3/notion/a.png"},
		{"", ""},
	}

	for _, testCase := range cases {
		if got := stripURLQuery(testCase.rawURL); got != testCase.want {
			t.Errorf("stripURLQuery(%q) = %q, want %q", testCase.rawURL, got, testCase.want)
		}
	}
}

// TestSaveFileRegistryStripsSignedQuery makes sure the churny pre-signed S3
// query never reaches the shard.
func TestSaveFileRegistryStripsSignedQuery(t *testing.T) {
	t.Parallel()

	crawler, _, tmpDir := newShardTestCrawler(t)
	ctx := context.Background()

	fileID := "ab112233445566778899aabbccddeeff"
	if err := crawler.saveFileRegistry(ctx, &FileRegistry{
		ID:        fileID,
		FilePath:  "tech/files/a.png",
		SourceURL: "https://s3/notion/a.png?X-Amz-Signature=deadbeef",
	}); err != nil {
		t.Fatalf("saveFileRegistry: %v", err)
	}
	if err := crawler.FlushRegistries(ctx); err != nil {
		t.Fatalf("FlushRegistries: %v", err)
	}

	content := readShardFile(t, tmpDir, fileShardDir, "ab")
	if strings.Contains(content, "X-Amz-Signature") {
		t.Errorf("shard still holds the signed query: %s", content)
	}
	if !strings.Contains(content, "https://s3/notion/a.png") {
		t.Errorf("shard lost the bare object path: %s", content)
	}
}

// TestCompactMigratesLegacyRegistries is the migration acceptance test:
// `reindex --compact` converts every legacy file, writes the shards and removes
// the old files.
func TestCompactMigratesLegacyRegistries(t *testing.T) {
	t.Parallel()

	crawler, st, tmpDir := newShardTestCrawler(t)
	ctx := context.Background()

	pageID := "aa11111111111111111111111111aaaa"
	otherID := "bb22222222222222222222222222bbbb"
	fileID := "ab112233445566778899aabbccddeeff"

	writeLegacyFile(t, tmpDir, "page-"+pageID+".json",
		`{"id":"`+pageID+`","type":"page","folder":"tech","file_path":"tech/a.md","title":"A"}`)
	writeLegacyFile(t, tmpDir, "page-"+otherID+".json",
		`{"id":"`+otherID+`","type":"page","folder":"tech","file_path":"tech/b.md","title":"B"}`)
	writeLegacyFile(t, tmpDir, "file-"+fileID+".json",
		`{"id":"`+fileID+`","file_path":"tech/files/a.png","source_url":"https://s3/a.png?X-Amz-Signature=x"}`)
	writeLegacyFile(t, tmpDir, "user-u1.json", `{"id":"u1","name":"Alice"}`)

	if err := crawler.Compact(ctx, false); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	for _, name := range []string{
		"page-" + pageID + ".json", "page-" + otherID + ".json",
		"file-" + fileID + ".json", "user-u1.json",
	} {
		if _, err := os.Stat(filepath.Join(tmpDir, ".notion-sync/ids", name)); !os.IsNotExist(err) {
			t.Errorf("legacy file %s must be deleted, stat err = %v", name, err)
		}
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".notion-sync/ids", idsManifestFile)); err != nil {
		t.Errorf("manifest must be written: %v", err)
	}

	if content := readShardFile(t, tmpDir, fileShardDir, "ab"); strings.Contains(content, "X-Amz-Signature") {
		t.Errorf("migration must normalize source_url: %s", content)
	}

	// A brand-new crawler must see everything through the shards alone.
	reader := NewCrawler(nil, st, WithCrawlerLogger(slog.Default()))

	registries, err := reader.listPageRegistries(ctx)
	if err != nil {
		t.Fatalf("listPageRegistries: %v", err)
	}
	if len(registries) != 2 {
		t.Fatalf("got %d page registries after compaction, want 2", len(registries))
	}

	if _, err := reader.loadFileRegistry(ctx, fileID); err != nil {
		t.Errorf("loadFileRegistry after compaction: %v", err)
	}
	if _, err := reader.loadUserRegistry(ctx, "u1"); err != nil {
		t.Errorf("loadUserRegistry after compaction: %v", err)
	}
}

// TestCompactDryRunChangesNothing checks the preview mode.
func TestCompactDryRunChangesNothing(t *testing.T) {
	t.Parallel()

	crawler, _, tmpDir := newShardTestCrawler(t)
	ctx := context.Background()

	pageID := "aa11111111111111111111111111aaaa"
	writeLegacyFile(t, tmpDir, "page-"+pageID+".json",
		`{"id":"`+pageID+`","type":"page","folder":"tech","file_path":"tech/a.md"}`)

	if err := crawler.Compact(ctx, true); err != nil {
		t.Fatalf("Compact(dryRun): %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".notion-sync/ids", "page-"+pageID+".json")); err != nil {
		t.Errorf("dry run must keep the legacy file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".notion-sync/ids", pageShardDir)); !os.IsNotExist(err) {
		t.Errorf("dry run must not write shards, stat err = %v", err)
	}
}
