package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

// requestsOfKind returns the recorded requests matching a method and a path
// fragment, in order.
func requestsOfKind(fake *fakeGitHub, method, fragment string) []recordedRequest {
	var out []recordedRequest
	for _, req := range fake.recorded() {
		if req.Method == method && strings.Contains(req.Path, fragment) {
			out = append(out, req)
		}
	}
	return out
}

// decodeTreeRequest parses the body of a tree creation request.
func decodeTreeRequest(t *testing.T, body []byte) struct {
	BaseTree string                   `json:"base_tree"`
	Tree     []gitHubTreeRequestEntry `json:"tree"`
} {
	t.Helper()

	var payload struct {
		BaseTree string                   `json:"base_tree"`
		Tree     []gitHubTreeRequestEntry `json:"tree"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode tree request: %v", err)
	}

	return payload
}

func TestGitHubCommitIssuesExactRequestSequence(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})
	ctx := context.Background()

	// Warm the head so the commit itself is the only traffic we count.
	if _, err := storeInst.Read(ctx, "root.md"); err != nil {
		t.Fatalf("read: %v", err)
	}

	baseline := len(fake.recorded())

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	if err := tx.Mkdir(ctx, "tech"); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := tx.Write(ctx, "tech/a.md", []byte("A")); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := tx.Write(ctx, "tech/b.md", []byte("B")); err != nil {
		t.Fatalf("write b: %v", err)
	}

	if err := tx.Commit(ctx, "two pages"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	total := len(fake.recorded()) - baseline
	if total != 5 {
		t.Fatalf("expected exactly 5 requests (2 blobs, 1 tree, 1 commit, 1 ref), got %d", total)
	}

	if got := len(requestsOfKind(fake, http.MethodPost, "/git/blobs")); got != 2 {
		t.Fatalf("expected 2 blob POSTs, got %d", got)
	}

	trees := requestsOfKind(fake, http.MethodPost, "/git/trees")
	if len(trees) != 1 {
		t.Fatalf("expected 1 tree POST, got %d", len(trees))
	}

	payload := decodeTreeRequest(t, trees[0].Body)
	if payload.BaseTree == "" {
		t.Fatal("tree POST must carry base_tree so unchanged paths are inherited")
	}
	if len(payload.Tree) != 2 {
		t.Fatalf("tree POST must carry only the changed paths, got %d", len(payload.Tree))
	}

	if got := len(requestsOfKind(fake, http.MethodPost, "/git/commits")); got != 1 {
		t.Fatalf("expected 1 commit POST, got %d", got)
	}
	if got := len(requestsOfKind(fake, http.MethodPatch, "/git/refs/heads/")); got != 1 {
		t.Fatalf("expected 1 ref PATCH, got %d", got)
	}

	files := fake.filesAt("main")
	if files["tech/a.md"] != "A" || files["tech/b.md"] != "B" || files["root.md"] != "# Root" {
		t.Fatalf("unexpected repository content after commit: %v", files)
	}
}

func TestGitHubCommitNeverForcesTheRef(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})
	ctx := context.Background()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := tx.Write(ctx, "a.md", []byte("A")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tx.Commit(ctx, "one page"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	for _, req := range requestsOfKind(fake, http.MethodPatch, "/git/refs/heads/") {
		if bytes.Contains(req.Body, []byte(`"force":true`)) {
			t.Fatalf("ref update must never force: %s", req.Body)
		}
		if !bytes.Contains(req.Body, []byte(`"force":false`)) {
			t.Fatalf("ref update must state force:false explicitly: %s", req.Body)
		}
	}

	if fake.forceSeen {
		t.Fatal("the API saw a forced ref update")
	}
}

// TestGitHubReadsSeePendingWritesWithinOneCycle is the single most important
// correctness property of this backend: a read-modify-write of the same
// registry shard twice inside one commit cycle must keep both records.
func TestGitHubReadsSeePendingWritesWithinOneCycle(t *testing.T) {
	t.Parallel()

	const shard = ".notion-sync/ids/page/ab.jsonl"

	_, storeInst := seededStore(t, map[string]string{shard: "{\"id\":\"first\"}\n"})
	ctx := context.Background()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	appendRecord := func(record string) {
		existing, readErr := storeInst.Read(ctx, shard)
		if readErr != nil {
			t.Fatalf("read shard: %v", readErr)
		}
		updated := append(append([]byte{}, existing...), []byte(record+"\n")...)
		if writeErr := tx.Write(ctx, shard, updated); writeErr != nil {
			t.Fatalf("write shard: %v", writeErr)
		}
	}

	appendRecord(`{"id":"second"}`)
	appendRecord(`{"id":"third"}`)

	// Before the commit, the store must already serve the buffered version.
	pending, err := storeInst.Read(ctx, shard)
	if err != nil {
		t.Fatalf("read pending shard: %v", err)
	}
	if strings.Count(string(pending), "\n") != 3 {
		t.Fatalf("pending read lost a write: %q", pending)
	}

	if err := tx.Commit(ctx, "two records"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	committed := readBack(t, storeInst, shard)
	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(committed, want) {
			t.Fatalf("committed shard lost %q: %q", want, committed)
		}
	}
}

// readBack reads a path back through the store after a commit.
func readBack(t *testing.T, storeInst *GitHubStore, path string) string {
	t.Helper()

	data, err := storeInst.Read(context.Background(), path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}

func TestGitHubPendingWritesAreVisibleToExistsAndList(t *testing.T) {
	t.Parallel()

	_, storeInst := seededStore(t, map[string]string{"tech/a.md": "A"})
	ctx := context.Background()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	if writeErr := tx.Write(ctx, "tech/b.md", []byte("B")); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}
	if delErr := tx.Delete(ctx, "tech/a.md"); delErr != nil {
		t.Fatalf("delete: %v", delErr)
	}

	exists, err := storeInst.Exists(ctx, "tech/b.md")
	if err != nil || !exists {
		t.Fatalf("pending write must be visible to Exists: %v %v", exists, err)
	}

	exists, err = storeInst.Exists(ctx, "tech/a.md")
	if err != nil || exists {
		t.Fatalf("pending delete must be visible to Exists: %v %v", exists, err)
	}

	entries, err := storeInst.List(ctx, "tech")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	names := map[string]bool{}
	for _, entry := range entries {
		names[entry.Path] = true
	}
	if !names["tech/b.md"] || names["tech/a.md"] {
		t.Fatalf("listing must reflect pending changes, got %v", names)
	}
}

func TestGitHubDeleteEmitsNullShaAndRemovesThePath(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{
		"tech/a.md": "A",
		"tech/b.md": "B",
	})
	ctx := context.Background()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := tx.Delete(ctx, "tech/a.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := tx.Commit(ctx, "drop a"); err != nil {
		t.Fatalf("commit: %v", err)
	}

	trees := requestsOfKind(fake, http.MethodPost, "/git/trees")
	if len(trees) != 1 {
		t.Fatalf("expected 1 tree POST, got %d", len(trees))
	}

	if !bytes.Contains(trees[0].Body, []byte(`"sha":null`)) {
		t.Fatalf("deletion must be a tree entry with a null sha, got %s", trees[0].Body)
	}

	payload := decodeTreeRequest(t, trees[0].Body)
	if len(payload.Tree) != 1 || payload.Tree[0].Path != "tech/a.md" || payload.Tree[0].SHA != nil {
		t.Fatalf("unexpected tree entries: %+v", payload.Tree)
	}

	files := fake.filesAt("main")
	if _, present := files["tech/a.md"]; present {
		t.Fatalf("deleted path is still in the tree: %v", files)
	}
	if files["tech/b.md"] != "B" {
		t.Fatalf("unrelated path was lost: %v", files)
	}
}

func TestGitHubDeleteOfMissingPathIsDropped(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{"tech/a.md": "A"})
	ctx := context.Background()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := tx.Delete(ctx, "tech/gone.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := tx.Commit(ctx, "drop nothing"); err != nil {
		t.Fatalf("commit of a no-op deletion must succeed: %v", err)
	}

	if got := len(requestsOfKind(fake, http.MethodPost, "/git/trees")); got != 0 {
		t.Fatalf("a deletion of a missing path must not create a tree, got %d tree POSTs", got)
	}

	if files := fake.filesAt("main"); files["tech/a.md"] != "A" {
		t.Fatalf("repository content changed unexpectedly: %v", files)
	}
}

func TestGitHubCommitRebuildsWhenTheRefMoves(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})
	ctx := context.Background()

	// Warm the head, then let a concurrent writer move the branch between our
	// tree build and our ref update.
	if _, err := storeInst.Read(ctx, "root.md"); err != nil {
		t.Fatalf("read: %v", err)
	}

	fake.mu.Lock()
	fake.beforeRefUpdate = func(other *fakeGitHub) {
		other.seedFiles("main", map[string]string{
			"root.md":       "# Root",
			"concurrent.md": "written by someone else",
		})
	}
	fake.mu.Unlock()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := tx.Write(ctx, "ours.md", []byte("ours")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := tx.Commit(ctx, "our page"); err != nil {
		t.Fatalf("commit must survive a concurrent ref move: %v", err)
	}

	if got := len(requestsOfKind(fake, http.MethodPost, "/git/blobs")); got != 1 {
		t.Fatalf("blobs must be uploaded once and reused across rebuilds, got %d", got)
	}
	if got := len(requestsOfKind(fake, http.MethodPost, "/git/trees")); got != 2 {
		t.Fatalf("expected the tree to be rebuilt exactly once, got %d tree POSTs", got)
	}
	if fake.forceSeen {
		t.Fatal("the retry path must never force the ref")
	}

	files := fake.filesAt("main")
	if files["ours.md"] != "ours" {
		t.Fatalf("our write was lost: %v", files)
	}
	if files["concurrent.md"] != "written by someone else" {
		t.Fatalf("the concurrent writer's commit was discarded: %v", files)
	}
}

func TestGitHubCommitGivesUpAfterBoundedRetries(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})
	ctx := context.Background()

	for range gitHubCommitAttempts {
		fake.script("PATCH /git/refs/heads/main", scriptedResponse{
			status: http.StatusUnprocessableEntity,
			body:   `{"message":"Update is not a fast forward"}`,
		})
	}

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if writeErr := tx.Write(ctx, "a.md", []byte("A")); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}

	err = tx.Commit(ctx, "doomed")
	if !errors.Is(err, apperrors.ErrMaxRetriesExceeded) {
		t.Fatalf("expected ErrMaxRetriesExceeded, got %v", err)
	}
	if fake.forceSeen {
		t.Fatal("giving up must not escalate to a forced update")
	}
}

func TestGitHubCommitCreatesMissingBranch(t *testing.T) {
	t.Parallel()

	fake := newFakeGitHub()

	storeInst, err := newTestGitHubStore(fake, testRemoteConfig())
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := context.Background()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := tx.Write(ctx, "root.md", []byte("# Root")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tx.Commit(ctx, "initial"); err != nil {
		t.Fatalf("commit on an empty branch: %v", err)
	}

	if got := len(requestsOfKind(fake, http.MethodPost, "/git/refs")); got == 0 {
		t.Fatal("an absent branch must be created with POST /git/refs")
	}

	if files := fake.filesAt("main"); files["root.md"] != "# Root" {
		t.Fatalf("unexpected content: %v", files)
	}
}

func TestGitHubTransactionRollbackDiscardsBufferedWrites(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})
	ctx := context.Background()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if writeErr := tx.Write(ctx, "scratch.md", []byte("temp")); writeErr != nil {
		t.Fatalf("write: %v", writeErr)
	}

	if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
		t.Fatalf("rollback: %v", rollbackErr)
	}

	exists, err := storeInst.Exists(ctx, "scratch.md")
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if exists {
		t.Fatal("rolled back writes must not remain visible")
	}

	if got := len(requestsOfKind(fake, http.MethodPost, "/git/blobs")); got != 0 {
		t.Fatalf("rollback must not upload anything, got %d blob POSTs", got)
	}

	if err := tx.Write(ctx, "again.md", []byte("x")); !errors.Is(err, apperrors.ErrTransactionCommitted) {
		t.Fatalf("a closed transaction must reject writes, got %v", err)
	}
}

func TestGitHubTransactionReusableAfterCommit(t *testing.T) {
	t.Parallel()

	_, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})
	ctx := context.Background()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	if err := tx.Write(ctx, "one.md", []byte("1")); err != nil {
		t.Fatalf("write one: %v", err)
	}
	if err := tx.Commit(ctx, "one"); err != nil {
		t.Fatalf("commit one: %v", err)
	}

	if err := tx.Write(ctx, "two.md", []byte("2")); err != nil {
		t.Fatalf("write two: %v", err)
	}
	if err := tx.Commit(ctx, "two"); err != nil {
		t.Fatalf("commit two: %v", err)
	}

	if data, readErr := storeInst.Read(ctx, "one.md"); readErr != nil || string(data) != "1" {
		t.Fatalf("first commit lost: %q %v", data, readErr)
	}
	if data, readErr := storeInst.Read(ctx, "two.md"); readErr != nil || string(data) != "2" {
		t.Fatalf("second commit lost: %q %v", data, readErr)
	}
}

func TestGitHubCommitWithNoChangesIsANoOp(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})
	ctx := context.Background()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	baseline := len(fake.recorded())
	if err := tx.Commit(ctx, "nothing"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(fake.recorded()) != baseline {
		t.Fatal("an empty commit must not touch the API")
	}
}

func TestGitHubWriteStreamBuffersAndCaps(t *testing.T) {
	t.Parallel()

	_, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})
	ctx := context.Background()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	written, err := tx.WriteStream(ctx, "attach/file.bin", strings.NewReader("payload"))
	if err != nil {
		t.Fatalf("write stream: %v", err)
	}
	if written != int64(len("payload")) {
		t.Fatalf("expected %d bytes written, got %d", len("payload"), written)
	}

	data, err := storeInst.Read(ctx, "attach/file.bin")
	if err != nil || string(data) != "payload" {
		t.Fatalf("streamed write must be readable: %q %v", data, err)
	}
}

func TestGitHubTransactionBufferBudgetIsEnforced(t *testing.T) {
	t.Parallel()

	_, storeInst := seededStore(t, map[string]string{"root.md": "# Root"})
	ctx := context.Background()

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	// The budget is counted in base64-encoded bytes, so a raw payload of
	// three quarters of the cap already exceeds it.
	oversized := make([]byte, gitHubMaxBufferedBytes*3/4+1024)

	err = tx.Write(ctx, "big.bin", oversized)
	if !errors.Is(err, apperrors.ErrGitHubBufferFull) {
		t.Fatalf("expected ErrGitHubBufferFull once base64 inflation is accounted for, got %v", err)
	}
}

// TestGitHubDeletionSurvivesNoTreeLookupFailure is the data-safety guard for a
// backup tool: if the tree listing that decides whether a deletion target
// exists fails, the commit must fail loudly. It must never publish a tree that
// silently keeps a file the caller asked to delete.
func TestGitHubDeletionSurvivesNoTreeLookupFailure(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{
		"tech/a.md": "A",
		"tech/b.md": "B",
	})
	ctx := context.Background()

	// A 404 on the *nested* tech tree is an API anomaly, not an empty
	// directory. Scripting it by sha leaves the root tree fetch untouched.
	techTree := fake.subtreeSHA("main", "tech")
	if techTree == "" {
		t.Fatal("fixture must contain a tech subtree")
	}

	fake.script("GET /git/trees/"+techTree, scriptedResponse{
		status: http.StatusNotFound,
		body:   `{"message":"Not Found"}`,
	})

	tx, err := storeInst.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if delErr := tx.Delete(ctx, "tech/a.md"); delErr != nil {
		t.Fatalf("delete: %v", delErr)
	}

	err = tx.Commit(ctx, "drop a")
	if err == nil {
		t.Fatal("a failed deletion-target lookup must fail the commit, not drop the deletion")
	}
	if !strings.Contains(err.Error(), "tech/a.md") {
		t.Fatalf("the error must name the deletion target, got %q", err)
	}

	if got := len(requestsOfKind(fake, http.MethodPost, "/git/trees")); got != 0 {
		t.Fatalf("no tree may be published when a deletion could not be resolved, got %d tree POSTs", got)
	}
	if got := len(requestsOfKind(fake, http.MethodPatch, "/git/refs/heads/")); got != 0 {
		t.Fatalf("the branch must not move, got %d ref updates", got)
	}

	// The buffered deletion is still pending, so a later retry can apply it.
	exists, existsErr := storeInst.Exists(ctx, "tech/a.md")
	if existsErr != nil {
		t.Fatalf("exists: %v", existsErr)
	}
	if exists {
		t.Fatal("the deletion must remain buffered after a failed commit")
	}
}

// TestGitHubMissingNestedTreeSurfacesAsAnError covers the read side of the same
// anomaly: a nested tree that cannot be listed is an error, not "not found".
func TestGitHubMissingNestedTreeSurfacesAsAnError(t *testing.T) {
	t.Parallel()

	fake, storeInst := seededStore(t, map[string]string{"tech/a.md": "A"})
	ctx := context.Background()

	techTree := fake.subtreeSHA("main", "tech")

	fake.script("GET /git/trees/"+techTree, scriptedResponse{
		status: http.StatusNotFound,
		body:   `{"message":"Not Found"}`,
	})

	if _, err := storeInst.Read(ctx, "tech/a.md"); err == nil {
		t.Fatal("an unlistable nested tree must surface as an error, not a missing file")
	} else if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("an API anomaly must not masquerade as fs.ErrNotExist: %v", err)
	}

	fake.script("GET /git/trees/"+techTree, scriptedResponse{
		status: http.StatusNotFound,
		body:   `{"message":"Not Found"}`,
	})

	if _, err := storeInst.Exists(ctx, "tech/a.md"); err == nil {
		t.Fatal("Exists must report the anomaly rather than answering false")
	}
}
