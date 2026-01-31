package sync

import (
	"testing"
)

func TestParseRootMdContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  string
		expected *RootManifest
		wantErr  bool
	}{
		{
			name: "valid task list with entries",
			content: `# Root Pages

- [x] **tech**: https://notion.so/Wiki-2c536f5e48f44234ad8d73a1a148e95d
- [ ] **product**: https://notion.so/Product-abc123def456789012345678901234ab
`,
			expected: &RootManifest{
				Entries: []RootEntry{
					{
						Folder:  "tech",
						Enabled: true,
						URL:     "https://notion.so/Wiki-2c536f5e48f44234ad8d73a1a148e95d",
						PageID:  "2c536f5e48f44234ad8d73a1a148e95d",
					},
					{
						Folder:  "product",
						Enabled: false,
						URL:     "https://notion.so/Product-abc123def456789012345678901234ab",
						PageID:  "abc123def456789012345678901234ab",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "empty file",
			content: `# Root Pages

`,
			expected: &RootManifest{
				Entries: nil,
			},
			wantErr: false,
		},
		{
			name: "no task list entries",
			content: `# Root Pages

Some text without task list entries
`,
			expected: &RootManifest{
				Entries: nil,
			},
			wantErr: false,
		},
		{
			name: "uppercase checkbox",
			content: `# Root Pages

- [X] **docs**: https://notion.so/Docs-aabbccdd11223344556677889900aabb
`,
			expected: &RootManifest{
				Entries: []RootEntry{
					{
						Folder:  "docs",
						Enabled: true,
						URL:     "https://notion.so/Docs-aabbccdd11223344556677889900aabb",
						PageID:  "aabbccdd11223344556677889900aabb",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "mixed enabled and disabled",
			content: `# Root Pages

- [x] **active**: https://notion.so/Active-1111111111111111111111111111111a
- [ ] **inactive**: https://notion.so/Inactive-2222222222222222222222222222222b
- [x] **also-active**: https://notion.so/Also-3333333333333333333333333333333c
`,
			expected: &RootManifest{
				Entries: []RootEntry{
					{
						Folder:  "active",
						Enabled: true,
						URL:     "https://notion.so/Active-1111111111111111111111111111111a",
						PageID:  "1111111111111111111111111111111a",
					},
					{
						Folder:  "inactive",
						Enabled: false,
						URL:     "https://notion.so/Inactive-2222222222222222222222222222222b",
						PageID:  "2222222222222222222222222222222b",
					},
					{
						Folder:  "also-active",
						Enabled: true,
						URL:     "https://notion.so/Also-3333333333333333333333333333333c",
						PageID:  "3333333333333333333333333333333c",
					},
				},
			},
			wantErr: false,
		},
		{
			name: "with extra whitespace",
			content: `# Root Pages

- [x] **tech**:   https://notion.so/Wiki-2c536f5e48f44234ad8d73a1a148e95d
`,
			expected: &RootManifest{
				Entries: []RootEntry{
					{
						Folder:  "tech",
						Enabled: true,
						URL:     "https://notion.so/Wiki-2c536f5e48f44234ad8d73a1a148e95d",
						PageID:  "2c536f5e48f44234ad8d73a1a148e95d",
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseRootMdContent([]byte(tt.content))
			if (err != nil) != tt.wantErr {
				t.Errorf("parseRootMdContent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if len(got.Entries) != len(tt.expected.Entries) {
				t.Errorf("parseRootMdContent() got %d entries, want %d", len(got.Entries), len(tt.expected.Entries))
				return
			}
			for i, entry := range got.Entries {
				exp := tt.expected.Entries[i]
				if entry.Folder != exp.Folder {
					t.Errorf("entry[%d].Folder = %q, want %q", i, entry.Folder, exp.Folder)
				}
				if entry.Enabled != exp.Enabled {
					t.Errorf("entry[%d].Enabled = %v, want %v", i, entry.Enabled, exp.Enabled)
				}
				if entry.URL != exp.URL {
					t.Errorf("entry[%d].URL = %q, want %q", i, entry.URL, exp.URL)
				}
				if entry.PageID != exp.PageID {
					t.Errorf("entry[%d].PageID = %q, want %q", i, entry.PageID, exp.PageID)
				}
			}
		})
	}
}

func TestFormatRootMd(t *testing.T) {
	t.Parallel()

	manifest := &RootManifest{
		Entries: []RootEntry{
			{
				Folder:  "tech",
				Enabled: true,
				URL:     "https://notion.so/Wiki-abc123",
			},
			{
				Folder:  "product",
				Enabled: false,
				URL:     "https://notion.so/Product-def456",
			},
		},
	}

	expected := `# Root Pages

- [x] **tech**: https://notion.so/Wiki-abc123
- [ ] **product**: https://notion.so/Product-def456
`

	got := formatRootMd(manifest)
	if got != expected {
		t.Errorf("formatRootMd() = %q, want %q", got, expected)
	}
}

func TestParseTaskListEntry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		line     string
		expected *RootEntry
		wantErr  bool
		wantNil  bool
	}{
		{
			name:    "valid enabled entry",
			line:    "- [x] **docs**: https://notion.so/Docs-aabbccdd11223344556677889900aabb",
			wantErr: false,
			expected: &RootEntry{
				Folder:  "docs",
				Enabled: true,
				URL:     "https://notion.so/Docs-aabbccdd11223344556677889900aabb",
				PageID:  "aabbccdd11223344556677889900aabb",
			},
		},
		{
			name:    "valid disabled entry",
			line:    "- [ ] **archive**: https://notion.so/Old-11223344556677889900112233445566",
			wantErr: false,
			expected: &RootEntry{
				Folder:  "archive",
				Enabled: false,
				URL:     "https://notion.so/Old-11223344556677889900112233445566",
				PageID:  "11223344556677889900112233445566",
			},
		},
		{
			name:    "invalid url",
			line:    "- [x] **docs**: not-a-valid-url",
			wantErr: true,
		},
		{
			name:    "non-matching line",
			line:    "Some random text",
			wantNil: true,
		},
		{
			name:    "empty line",
			line:    "",
			wantNil: true,
		},
		{
			name:    "header line",
			line:    "# Root Pages",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseTaskListEntry(tt.line)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseTaskListEntry() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}
			if tt.wantNil {
				if got != nil {
					t.Errorf("parseTaskListEntry() = %v, want nil", got)
				}
				return
			}
			if got.Folder != tt.expected.Folder {
				t.Errorf("Folder = %q, want %q", got.Folder, tt.expected.Folder)
			}
			if got.Enabled != tt.expected.Enabled {
				t.Errorf("Enabled = %v, want %v", got.Enabled, tt.expected.Enabled)
			}
			if got.URL != tt.expected.URL {
				t.Errorf("URL = %q, want %q", got.URL, tt.expected.URL)
			}
			if got.PageID != tt.expected.PageID {
				t.Errorf("PageID = %q, want %q", got.PageID, tt.expected.PageID)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	original := &RootManifest{
		Entries: []RootEntry{
			{
				Folder:  "tech",
				Enabled: true,
				URL:     "https://notion.so/Wiki-2c536f5e48f44234ad8d73a1a148e95d",
				PageID:  "2c536f5e48f44234ad8d73a1a148e95d",
			},
			{
				Folder:  "product",
				Enabled: false,
				URL:     "https://notion.so/Product-abc123def456789012345678901234ab",
				PageID:  "abc123def456789012345678901234ab",
			},
		},
	}

	// Format to markdown
	formatted := formatRootMd(original)

	// Parse back
	parsed, err := parseRootMdContent([]byte(formatted))
	if err != nil {
		t.Fatalf("parseRootMdContent() error = %v", err)
	}

	// Compare
	if len(parsed.Entries) != len(original.Entries) {
		t.Fatalf("Round trip: got %d entries, want %d", len(parsed.Entries), len(original.Entries))
	}

	for i, entry := range parsed.Entries {
		exp := original.Entries[i]
		if entry.Folder != exp.Folder {
			t.Errorf("entry[%d].Folder = %q, want %q", i, entry.Folder, exp.Folder)
		}
		if entry.Enabled != exp.Enabled {
			t.Errorf("entry[%d].Enabled = %v, want %v", i, entry.Enabled, exp.Enabled)
		}
		if entry.URL != exp.URL {
			t.Errorf("entry[%d].URL = %q, want %q", i, entry.URL, exp.URL)
		}
		if entry.PageID != exp.PageID {
			t.Errorf("entry[%d].PageID = %q, want %q", i, entry.PageID, exp.PageID)
		}
	}
}
