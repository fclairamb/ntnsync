package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// recordedRequest is one request the fake GitHub API saw.
type recordedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Body     []byte
}

// fakeGitHub is an in-memory stand-in for the GitHub Git Data API, wired in as
// an http.RoundTripper. No test in this package touches the network.
type fakeGitHub struct {
	mu sync.Mutex

	blobs   map[string][]byte
	trees   map[string][]gitHubTreeEntry
	commits map[string][]string // commit sha -> parents
	trees4  map[string]string   // commit sha -> tree sha
	refs    map[string]string   // branch -> commit sha

	requests []recordedRequest

	// forceSeen records whether any ref update asked for force:true.
	forceSeen bool

	// beforeRefUpdate runs before a ref update is validated, letting a test
	// simulate a concurrent writer moving the branch.
	beforeRefUpdate func(f *fakeGitHub)

	// scripted lets a test force a response for the next call to a route.
	scripted map[string][]scriptedResponse

	// rateRemaining, when >= 0, is reported in X-RateLimit-Remaining.
	rateRemaining int
	rateLimit     int
	rateReset     time.Time
}

// scriptedResponse is a canned reply queued for one route.
type scriptedResponse struct {
	status  int
	body    string
	headers map[string]string
}

// newFakeGitHub creates an empty fake API.
func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{
		blobs:         map[string][]byte{},
		trees:         map[string][]gitHubTreeEntry{},
		commits:       map[string][]string{},
		trees4:        map[string]string{},
		refs:          map[string]string{},
		scripted:      map[string][]scriptedResponse{},
		rateRemaining: 4999,
		rateLimit:     5000,
		rateReset:     time.Unix(2_000_000_000, 0),
	}
}

// fakeSHA produces a deterministic 40-hex-character object id.
func fakeSHA(kind string, payload []byte) string {
	sum := sha256.Sum256(append([]byte(kind+":"), payload...))
	return hex.EncodeToString(sum[:])[:40]
}

// seedFiles builds a commit containing exactly the given path/content pairs and
// points branch at it.
func (f *fakeGitHub) seedFiles(branch string, files map[string]string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	flat := map[string]string{}
	for path, content := range files {
		blobSHA := fakeSHA("blob", []byte(content))
		f.blobs[blobSHA] = []byte(content)
		flat[path] = blobSHA
	}

	treeSHA := f.buildTreeLocked(flat)
	commitSHA := fakeSHA("commit", []byte(treeSHA+"|seed"))
	f.commits[commitSHA] = nil
	f.trees4[commitSHA] = treeSHA
	f.refs[branch] = commitSHA

	return commitSHA
}

// flattenLocked expands a tree into path -> blob sha.
func (f *fakeGitHub) flattenLocked(treeSHA, prefix string) map[string]string {
	out := map[string]string{}
	for _, entry := range f.trees[treeSHA] {
		full := entry.Path
		if prefix != "" {
			full = prefix + "/" + entry.Path
		}
		if entry.Type == gitHubTypeTree {
			maps.Copy(out, f.flattenLocked(entry.SHA, full))
			continue
		}
		out[full] = entry.SHA
	}
	return out
}

// buildTreeLocked builds nested trees from a flat path -> blob sha map and
// returns the root tree sha.
func (f *fakeGitHub) buildTreeLocked(flat map[string]string) string {
	return f.buildSubtreeLocked(flat, "")
}

// buildSubtreeLocked builds the tree for one directory prefix.
func (f *fakeGitHub) buildSubtreeLocked(flat map[string]string, prefix string) string {
	children := map[string]string{}
	dirs := map[string]bool{}

	for path, sha := range flat {
		rest := path
		if prefix != "" {
			if !strings.HasPrefix(path, prefix+"/") {
				continue
			}
			rest = strings.TrimPrefix(path, prefix+"/")
		}
		name, _, nested := strings.Cut(rest, "/")
		if nested {
			dirs[name] = true
			continue
		}
		children[name] = sha
	}

	entries := make([]gitHubTreeEntry, 0, len(children)+len(dirs))
	for name, sha := range children {
		entries = append(entries, gitHubTreeEntry{
			Path: name, Mode: gitHubBlobMode, Type: gitHubTypeBlob,
			SHA: sha, Size: int64(len(f.blobs[sha])),
		})
	}
	for name := range dirs {
		sub := prefix
		if sub == "" {
			sub = name
		} else {
			sub = prefix + "/" + name
		}
		entries = append(entries, gitHubTreeEntry{
			Path: name, Mode: gitHubTreeMode, Type: gitHubTypeTree,
			SHA: f.buildSubtreeLocked(flat, sub),
		})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	var fingerprint strings.Builder
	for _, entry := range entries {
		fingerprint.WriteString(entry.Path + ":" + entry.Type + ":" + entry.SHA + "\n")
	}
	treeSHA := fakeSHA("tree", []byte(fingerprint.String()))
	f.trees[treeSHA] = entries

	return treeSHA
}

// filesAt returns the flattened content of the branch head.
func (f *fakeGitHub) filesAt(branch string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()

	commitSHA, ok := f.refs[branch]
	if !ok {
		return nil
	}

	out := map[string]string{}
	for path, blobSHA := range f.flattenLocked(f.trees4[commitSHA], "") {
		out[path] = string(f.blobs[blobSHA])
	}

	return out
}

// subtreeSHA returns the tree sha of a directory on a branch, walking the
// nested trees exactly as the store does.
func (f *fakeGitHub) subtreeSHA(branch, dir string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	commitSHA, ok := f.refs[branch]
	if !ok {
		return ""
	}

	current := f.trees4[commitSHA]
	if dir == "" {
		return current
	}

	for segment := range strings.SplitSeq(dir, "/") {
		next := ""
		for _, entry := range f.trees[current] {
			if entry.Path == segment && entry.Type == gitHubTypeTree {
				next = entry.SHA
				break
			}
		}
		if next == "" {
			return ""
		}
		current = next
	}

	return current
}

// recorded returns a copy of the recorded requests.
func (f *fakeGitHub) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// script queues a canned response for "METHOD /suffix".
func (f *fakeGitHub) script(key string, resp scriptedResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripted[key] = append(f.scripted[key], resp)
}

// RoundTrip implements http.RoundTripper.
func (f *fakeGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}

	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		Method:   req.Method,
		Path:     req.URL.Path,
		RawQuery: req.URL.RawQuery,
		Body:     body,
	})
	f.mu.Unlock()

	if resp, ok := f.takeScripted(req); ok {
		return resp, nil
	}

	status, payload := f.dispatch(req, body)

	return f.respond(status, payload), nil
}

// takeScripted pops a queued canned response, if one matches.
func (f *fakeGitHub) takeScripted(req *http.Request) (*http.Response, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for key, queued := range f.scripted {
		method, suffix, _ := strings.Cut(key, " ")
		if req.Method != method || !strings.Contains(req.URL.Path, suffix) || len(queued) == 0 {
			continue
		}

		resp := queued[0]
		f.scripted[key] = queued[1:]

		header := http.Header{"Content-Type": []string{"application/json"}}
		for name, value := range resp.headers {
			header.Set(name, value)
		}

		return &http.Response{
			StatusCode: resp.status,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(resp.body)),
			Request:    req,
		}, true
	}

	return nil, false
}

// respond builds an HTTP response carrying rate-limit headers.
func (f *fakeGitHub) respond(status int, payload any) *http.Response {
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte(`{"message":"fake encode failure"}`)
		status = http.StatusInternalServerError
	}

	header := http.Header{"Content-Type": []string{"application/json"}}

	f.mu.Lock()
	if f.rateRemaining >= 0 {
		header.Set("X-Ratelimit-Remaining", strconv.Itoa(f.rateRemaining))
		header.Set("X-Ratelimit-Limit", strconv.Itoa(f.rateLimit))
		header.Set("X-Ratelimit-Reset", strconv.FormatInt(f.rateReset.Unix(), 10))
	}
	f.mu.Unlock()

	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(encoded)),
	}
}

// dispatch routes a request to the matching handler.
func (f *fakeGitHub) dispatch(req *http.Request, body []byte) (int, any) {
	f.mu.Lock()
	defer f.mu.Unlock()

	path := req.URL.Path

	switch {
	case req.Method == http.MethodGet && strings.Contains(path, "/git/ref/heads/"):
		return f.handleGetRef(path)
	case req.Method == http.MethodGet && strings.Contains(path, "/git/commits/"):
		return f.handleGetCommit(path)
	case req.Method == http.MethodGet && strings.Contains(path, "/git/trees/"):
		return f.handleGetTree(path)
	case req.Method == http.MethodGet && strings.Contains(path, "/git/blobs/"):
		return f.handleGetBlob(path)
	case req.Method == http.MethodPost && strings.HasSuffix(path, "/git/blobs"):
		return f.handleCreateBlob(body)
	case req.Method == http.MethodPost && strings.HasSuffix(path, "/git/trees"):
		return f.handleCreateTree(body)
	case req.Method == http.MethodPost && strings.HasSuffix(path, "/git/commits"):
		return f.handleCreateCommit(body)
	case req.Method == http.MethodPatch && strings.Contains(path, "/git/refs/heads/"):
		return f.handleUpdateRef(path, body)
	case req.Method == http.MethodPost && strings.HasSuffix(path, "/git/refs"):
		return f.handleCreateRef(body)
	}

	return http.StatusNotFound, map[string]string{"message": "no fake route for " + req.Method + " " + path}
}

func (f *fakeGitHub) handleGetRef(path string) (int, any) {
	branch := path[strings.Index(path, "/git/ref/heads/")+len("/git/ref/heads/"):]

	commitSHA, ok := f.refs[branch]
	if !ok {
		return http.StatusNotFound, map[string]string{"message": "Not Found"}
	}

	out := gitHubRefResponse{Ref: "refs/heads/" + branch}
	out.Object.SHA = commitSHA
	out.Object.Type = "commit"

	return http.StatusOK, out
}

func (f *fakeGitHub) handleGetCommit(path string) (int, any) {
	sha := path[strings.LastIndex(path, "/")+1:]

	treeSHA, ok := f.trees4[sha]
	if !ok {
		return http.StatusNotFound, map[string]string{"message": "Not Found"}
	}

	out := gitHubCommitResponse{SHA: sha}
	out.Tree.SHA = treeSHA

	return http.StatusOK, out
}

func (f *fakeGitHub) handleGetTree(path string) (int, any) {
	sha := path[strings.LastIndex(path, "/")+1:]

	entries, ok := f.trees[sha]
	if !ok {
		return http.StatusNotFound, map[string]string{"message": "Not Found"}
	}

	return http.StatusOK, gitHubTreeResponse{SHA: sha, Tree: entries}
}

func (f *fakeGitHub) handleGetBlob(path string) (int, any) {
	sha := path[strings.LastIndex(path, "/")+1:]

	content, ok := f.blobs[sha]
	if !ok {
		return http.StatusNotFound, map[string]string{"message": "Not Found"}
	}

	return http.StatusOK, gitHubBlobResponse{
		SHA:      sha,
		Content:  base64.StdEncoding.EncodeToString(content),
		Encoding: "base64",
	}
}

func (f *fakeGitHub) handleCreateBlob(body []byte) (int, any) {
	var payload struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return http.StatusBadRequest, map[string]string{"message": err.Error()}
	}

	raw, err := base64.StdEncoding.DecodeString(payload.Content)
	if err != nil {
		return http.StatusBadRequest, map[string]string{"message": err.Error()}
	}

	sha := fakeSHA("blob", raw)
	f.blobs[sha] = raw

	return http.StatusCreated, gitHubBlobResponse{SHA: sha}
}

func (f *fakeGitHub) handleCreateTree(body []byte) (int, any) {
	var payload struct {
		BaseTree string                   `json:"base_tree"`
		Tree     []gitHubTreeRequestEntry `json:"tree"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return http.StatusBadRequest, map[string]string{"message": err.Error()}
	}

	flat := map[string]string{}
	if payload.BaseTree != "" {
		if _, ok := f.trees[payload.BaseTree]; !ok {
			return http.StatusUnprocessableEntity, map[string]string{"message": "base_tree not found"}
		}
		flat = f.flattenLocked(payload.BaseTree, "")
	}

	for _, entry := range payload.Tree {
		if entry.SHA == nil {
			if _, ok := flat[entry.Path]; !ok {
				return http.StatusUnprocessableEntity,
					map[string]string{"message": "cannot delete missing path " + entry.Path}
			}
			delete(flat, entry.Path)
			continue
		}
		flat[entry.Path] = *entry.SHA
	}

	return http.StatusCreated, gitHubTreeResponse{SHA: f.buildTreeLocked(flat)}
}

func (f *fakeGitHub) handleCreateCommit(body []byte) (int, any) {
	var payload struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return http.StatusBadRequest, map[string]string{"message": err.Error()}
	}

	sha := fakeSHA("commit", []byte(payload.Tree+"|"+payload.Message+"|"+strings.Join(payload.Parents, ",")))
	f.commits[sha] = payload.Parents
	f.trees4[sha] = payload.Tree

	out := gitHubCommitResponse{SHA: sha}
	out.Tree.SHA = payload.Tree

	return http.StatusCreated, out
}

func (f *fakeGitHub) handleUpdateRef(path string, body []byte) (int, any) {
	branch := path[strings.Index(path, "/git/refs/heads/")+len("/git/refs/heads/"):]

	var payload struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return http.StatusBadRequest, map[string]string{"message": err.Error()}
	}

	if payload.Force {
		f.forceSeen = true
	}

	if f.beforeRefUpdate != nil {
		hook := f.beforeRefUpdate
		f.beforeRefUpdate = nil
		f.mu.Unlock()
		hook(f)
		f.mu.Lock()
	}

	current, ok := f.refs[branch]
	if !ok {
		return http.StatusUnprocessableEntity, map[string]string{"message": "ref does not exist"}
	}

	if !payload.Force && !f.isDescendantLocked(payload.SHA, current) {
		return http.StatusUnprocessableEntity,
			map[string]string{"message": "Update is not a fast forward"}
	}

	f.refs[branch] = payload.SHA

	out := gitHubRefResponse{Ref: "refs/heads/" + branch}
	out.Object.SHA = payload.SHA

	return http.StatusOK, out
}

func (f *fakeGitHub) handleCreateRef(body []byte) (int, any) {
	var payload struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return http.StatusBadRequest, map[string]string{"message": err.Error()}
	}

	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	if _, exists := f.refs[branch]; exists {
		return http.StatusUnprocessableEntity, map[string]string{"message": "Reference already exists"}
	}

	f.refs[branch] = payload.SHA

	out := gitHubRefResponse{Ref: payload.Ref}
	out.Object.SHA = payload.SHA

	return http.StatusCreated, out
}

// isDescendantLocked reports whether candidate has ancestor in its history.
func (f *fakeGitHub) isDescendantLocked(candidate, ancestor string) bool {
	seen := map[string]bool{}
	queue := []string{candidate}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if current == ancestor {
			return true
		}
		if seen[current] {
			continue
		}
		seen[current] = true
		queue = append(queue, f.commits[current]...)
	}

	return false
}

// newTestGitHubStore builds a GitHubStore wired to a fake API.
func newTestGitHubStore(fake *fakeGitHub, cfg *RemoteConfig, opts ...GitHubStoreOption) (*GitHubStore, error) {
	base := make([]GitHubStoreOption, 0, 1+len(opts))
	base = append(base, withGitHubAPIOptions(
		withGitHubTransport(fake),
		withGitHubBaseURL("https://api.github.test"),
		withGitHubSleep(func(context.Context, time.Duration) error { return nil }),
	))

	return NewGitHubStore(cfg, append(base, opts...)...)
}

// testRemoteConfig returns a github-mode configuration for tests.
func testRemoteConfig() *RemoteConfig {
	return &RemoteConfig{
		Storage:  StorageModeGitHub,
		URL:      "https://github.com/acme/wiki.git",
		Password: "test-token",
		Branch:   "main",
		User:     "ntnsync",
		Email:    "ntnsync@local",
	}
}
