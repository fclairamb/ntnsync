package store

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"testing"
)

// seededStore returns a store whose branch already holds the given files.
func seededStore(t *testing.T, files map[string]string) (*fakeGitHub, *GitHubStore) {
	t.Helper()

	fake := newFakeGitHub()
	fake.seedFiles("main", files)

	storeInst, err := newTestGitHubStore(fake, testRemoteConfig())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	return fake, storeInst
}

func TestGitHubStoreReadDescendsLazily(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{
		"root.md":                        "# Root",
		"tech/wiki/page.md":              "# Page",
		".notion-sync/ids/page/ab.jsonl": `{"id":"ab1"}`,
	})

	ctx := context.Background()

	data, err := storeInst.Read(ctx, "tech/wiki/page.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "# Page" {
		t.Fatalf("unexpected content %q", data)
	}

	// One tree fetch per directory level walked: root, tech, tech/wiki.
	// Nothing else in the repository is transferred.
	treeFetches := 0
	for _, req := range fake.recorded() {
		if req.Method == http.MethodGet && strings.Contains(req.Path, "/git/trees/") {
			treeFetches++
		}
	}
	if treeFetches != 3 {
		t.Fatalf("expected 3 lazy tree fetches (root, tech, tech/wiki), got %d", treeFetches)
	}
}

func TestGitHubStoreNeverRequestsRecursiveTree(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{
		"root.md":                        "# Root",
		"tech/wiki/deep/page.md":         "# Deep",
		".notion-sync/ids/page/ab.jsonl": `{"id":"ab1"}`,
	})

	ctx := context.Background()

	if _, err := storeInst.Read(ctx, "tech/wiki/deep/page.md"); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := storeInst.List(ctx, "tech/wiki"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := storeInst.Exists(ctx, ".notion-sync/ids/page/ab.jsonl"); err != nil {
		t.Fatalf("exists: %v", err)
	}
	if _, err := storeInst.List(ctx, ""); err != nil {
		t.Fatalf("list root: %v", err)
	}

	for _, req := range fake.recorded() {
		if strings.Contains(req.RawQuery, "recursive") {
			t.Fatalf("request %s %s?%s used a recursive tree listing", req.Method, req.Path, req.RawQuery)
		}
		if strings.Contains(req.Path, "recursive") {
			t.Fatalf("request %s %s used a recursive tree listing", req.Method, req.Path)
		}
	}
}

func TestGitHubStoreReadMissingFile(t *testing.T) {
	t.Parallel()

	_, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})

	ctx := context.Background()

	if _, err := storeInst.Read(ctx, "missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}

	exists, err := storeInst.Exists(ctx, "missing.md")
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if exists {
		t.Fatal("missing file must not report as existing")
	}
}

func TestGitHubStoreListDirectory(t *testing.T) {
	t.Parallel()

	_, storeInst := seededStore(t, map[string]string{
		"tech/a.md":       "a",
		"tech/b.md":       "b",
		"tech/sub/c.md":   "c",
		"product/root.md": "r",
	})

	entries, err := storeInst.List(context.Background(), "tech")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	got := map[string]bool{}
	for _, entry := range entries {
		got[entry.Path] = entry.IsDir
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d (%v)", len(got), got)
	}
	if isDir, ok := got["tech/a.md"]; !ok || isDir {
		t.Fatalf("expected tech/a.md as a file, got %v", got)
	}
	if isDir, ok := got["tech/sub"]; !ok || !isDir {
		t.Fatalf("expected tech/sub as a directory, got %v", got)
	}
}

func TestGitHubStoreListMissingDirectory(t *testing.T) {
	t.Parallel()

	_, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})

	entries, err := storeInst.List(context.Background(), "nope")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if entries != nil {
		t.Fatalf("expected nil listing for a missing directory, got %v", entries)
	}
}

func TestGitHubStoreCachesShardBodiesOnly(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{
		".notion-sync/ids/page/ab.jsonl": `{"id":"ab1"}`,
		"tech/page.md":                   "# Page",
	})

	ctx := context.Background()

	for range 3 {
		if _, err := storeInst.Read(ctx, ".notion-sync/ids/page/ab.jsonl"); err != nil {
			t.Fatalf("read shard: %v", err)
		}
	}

	shardBlobFetches := countBlobFetches(fake)
	if shardBlobFetches != 1 {
		t.Fatalf("expected the shard body to be cached after one fetch, got %d fetches", shardBlobFetches)
	}

	for range 3 {
		if _, err := storeInst.Read(ctx, "tech/page.md"); err != nil {
			t.Fatalf("read page: %v", err)
		}
	}

	if total := countBlobFetches(fake); total != 4 {
		t.Fatalf("page content must not be cached: expected 1+3 blob fetches, got %d", total)
	}
}

// countBlobFetches counts GET requests against the blob endpoint.
func countBlobFetches(fake *fakeGitHub) int {
	count := 0
	for _, req := range fake.recorded() {
		if req.Method == http.MethodGet && strings.Contains(req.Path, "/git/blobs/") {
			count++
		}
	}
	return count
}

func TestGitHubStorePushIsNoOpAndPullRefreshesHead(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{"root.md": "v1"})

	ctx := context.Background()

	if _, err := storeInst.Read(ctx, "root.md"); err != nil {
		t.Fatalf("read: %v", err)
	}

	before := len(fake.recorded())
	if err := storeInst.Push(ctx); err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(fake.recorded()) != before {
		t.Fatal("Push must not issue any API request")
	}

	// A concurrent writer replaces the branch content.
	fake.seedFiles("main", map[string]string{"root.md": "v2"})

	if err := storeInst.Pull(ctx); err != nil {
		t.Fatalf("pull: %v", err)
	}

	data, err := storeInst.Read(ctx, "root.md")
	if err != nil {
		t.Fatalf("read after pull: %v", err)
	}
	if string(data) != "v2" {
		t.Fatalf("pull must refresh the cached head, got %q", data)
	}
}

func TestGitHubStoreEmptyBranch(t *testing.T) {
	t.Parallel()

	fake := newFakeGitHub()

	storeInst, err := newTestGitHubStore(fake, testRemoteConfig())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := context.Background()

	exists, err := storeInst.Exists(ctx, "root.md")
	if err != nil {
		t.Fatalf("exists on empty branch: %v", err)
	}
	if exists {
		t.Fatal("nothing exists on an empty branch")
	}

	entries, err := storeInst.List(ctx, "")
	if err != nil {
		t.Fatalf("list on empty branch: %v", err)
	}
	if entries != nil {
		t.Fatalf("expected empty listing, got %v", entries)
	}
}

func TestGitHubStoreRejectsWholeTreeCommands(t *testing.T) {
	t.Parallel()

	_, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})

	for _, command := range []string{"reindex", "cleanup", "list --tree"} {
		err := storeInst.CheckWholeTreeSupported(command)
		if err == nil {
			t.Fatalf("%s must be rejected in github mode", command)
		}
		if !strings.Contains(err.Error(), "NTN_STORAGE=github") {
			t.Fatalf("error must name the storage mode, got %q", err.Error())
		}
		if !strings.Contains(err.Error(), command) {
			t.Fatalf("error must name the command, got %q", err.Error())
		}
	}
}

func TestGitHubStoreSplitStoreRouting(t *testing.T) {
	t.Parallel()

	fake := newFakeGitHub()
	fake.seedFiles("main", map[string]string{"root.md": "# Root"})
	fake.seedFiles("queue", map[string]string{".notion-sync/queue/tech-init.json": "[]"})

	cfg := testRemoteConfig()
	cfg.QueueBranch = "queue"

	contentStore, err := newTestGitHubStore(fake, cfg)
	if err != nil {
		t.Fatalf("create content store: %v", err)
	}

	queueStore, err := newTestGitHubStore(fake, cfg, WithGitHubBranch("queue"))
	if err != nil {
		t.Fatalf("create queue store: %v", err)
	}

	split := NewSplitStore(contentStore, queueStore)
	ctx := context.Background()

	if data, readErr := split.Read(ctx, "root.md"); readErr != nil || string(data) != "# Root" {
		t.Fatalf("content read routed wrong: %q %v", data, readErr)
	}
	if data, readErr := split.Read(ctx, ".notion-sync/queue/tech-init.json"); readErr != nil || string(data) != "[]" {
		t.Fatalf("queue read routed wrong: %q %v", data, readErr)
	}

	if capErr := split.CheckWholeTreeSupported("cleanup"); capErr == nil {
		t.Fatal("split store must delegate the whole-tree capability check")
	}

	// Writes must land on the branch the path routes to.
	tx, txErr := split.BeginTx(ctx)
	if txErr != nil {
		t.Fatalf("begin split tx: %v", txErr)
	}
	if writeErr := tx.Write(ctx, "tech/new.md", []byte("content")); writeErr != nil {
		t.Fatalf("write content: %v", writeErr)
	}
	if writeErr := tx.Write(ctx, ".notion-sync/queue/tech-update.json", []byte(`["id"]`)); writeErr != nil {
		t.Fatalf("write queue: %v", writeErr)
	}
	if commitErr := tx.Commit(ctx, "split write"); commitErr != nil {
		t.Fatalf("commit split tx: %v", commitErr)
	}

	mainFiles := fake.filesAt("main")
	if mainFiles["tech/new.md"] != "content" {
		t.Fatalf("content write did not land on main: %v", mainFiles)
	}
	if _, leaked := mainFiles[".notion-sync/queue/tech-update.json"]; leaked {
		t.Fatalf("queue write leaked onto the content branch: %v", mainFiles)
	}

	queueFiles := fake.filesAt("queue")
	if queueFiles[".notion-sync/queue/tech-update.json"] != `["id"]` {
		t.Fatalf("queue write did not land on the queue branch: %v", queueFiles)
	}

	// The SplitStore must accept any Store implementation, not just LocalStore.
	var _ RemoteStore = contentStore
	var _ RemoteStore = split
}

func TestGitHubBackendWritesNothingToTheFilesystem(t *testing.T) {
	t.Parallel()

	forbidden := []string{"os.WriteFile", "os.Create", "os.MkdirAll", "os.OpenFile"}

	for _, name := range []string{"github.go", "github_api.go", "github_tx.go", "github_lru.go"} {
		source, err := os.ReadFile(name) //nolint:gosec // fixed list of source files in this package
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, call := range forbidden {
			if strings.Contains(string(source), call) {
				t.Fatalf("%s must not write to the local filesystem, found %s", name, call)
			}
		}
	}
}
