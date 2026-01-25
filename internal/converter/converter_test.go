package converter

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fclairamb/ntnsync/internal/notion"
)

func TestNewConverter_DefaultSettings(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	if c == nil {
		t.Fatal("NewConverter() returned nil")
	}
	if !c.IncludeFrontmatter {
		t.Error("NewConverter() should have IncludeFrontmatter=true by default")
	}
}

func TestConvert_BasicPage(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	page := &notion.Page{
		ID:             "123e4567-e89b-12d3-a456-426614174000",
		LastEditedTime: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		URL:            "https://notion.so/test",
		Properties: map[string]notion.Property{
			"title": {
				ID:   "title",
				Type: "title",
				Title: []notion.RichText{
					{
						Type:      "text",
						PlainText: "Test Page",
					},
				},
			},
		},
	}
	blocks := []notion.Block{
		{
			Type: "paragraph",
			Paragraph: &notion.ParagraphBlock{
				RichText: []notion.RichText{
					{
						Type:      "text",
						PlainText: "Hello world",
					},
				},
			},
		},
	}

	result := c.Convert(page, blocks)
	resultStr := string(result)

	// Check frontmatter
	if !strings.Contains(resultStr, "---") {
		t.Error("Convert() should include frontmatter by default")
	}
	if !strings.Contains(resultStr, "notion_id: 123e4567-e89b-12d3-a456-426614174000") {
		t.Error("Convert() should include notion_id in frontmatter")
	}

	// Check title
	if !strings.Contains(resultStr, "# Test Page") {
		t.Error("Convert() should include page title as h1")
	}

	// Check content
	if !strings.Contains(resultStr, "Hello world") {
		t.Error("Convert() should include paragraph content")
	}
}

func TestConvert_NoFrontmatter(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	c.IncludeFrontmatter = false

	page := &notion.Page{
		ID:             "123e4567-e89b-12d3-a456-426614174000",
		LastEditedTime: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		URL:            "https://notion.so/test",
		Properties: map[string]notion.Property{
			"title": {
				ID:   "title",
				Type: "title",
				Title: []notion.RichText{
					{
						Type:      "text",
						PlainText: "Test Page",
					},
				},
			},
		},
	}
	blocks := []notion.Block{}

	result := c.Convert(page, blocks)
	resultStr := string(result)

	// Should not have frontmatter
	if strings.Contains(resultStr, "---") {
		t.Error("Convert() should not include frontmatter when disabled")
	}

	// Should still have title
	if !strings.Contains(resultStr, "# Test Page") {
		t.Error("Convert() should include page title even without frontmatter")
	}
}

func TestConvertWithOptions_AllFields(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	page := &notion.Page{
		ID:             "123e4567-e89b-12d3-a456-426614174000",
		LastEditedTime: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		URL:            "https://notion.so/test",
		Properties: map[string]notion.Property{
			"title": {
				ID:   "title",
				Type: "title",
				Title: []notion.RichText{
					{
						Type:      "text",
						PlainText: "Test Page",
					},
				},
			},
		},
	}
	blocks := []notion.Block{}
	opts := ConvertOptions{
		Folder:     "tech",
		PageTitle:  "Test Page",
		FilePath:   "tech/test-page.md",
		LastSynced: time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
		NotionType: "page",
		IsRoot:     true,
		ParentID:   "parent123",
	}

	result := c.ConvertWithOptions(page, blocks, opts)
	resultStr := string(result)

	// Check all fields are in frontmatter
	expectedFields := []string{
		"notion_folder: tech",
		"file_path: tech/test-page.md",
		"notion_type: page",
		"is_root: true",
		"notion_parent_id: parent123",
		"last_synced:",
	}

	for _, field := range expectedFields {
		if !strings.Contains(resultStr, field) {
			t.Errorf("ConvertWithOptions() missing field: %s", field)
		}
	}
}

func TestConvertDatabase_WithChildren(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	database := &notion.Database{
		ID:             "db123e4567-e89b-12d3-a456-426614174000",
		LastEditedTime: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		URL:            "https://notion.so/testdb",
		Title: []notion.RichText{
			{
				Type:      "text",
				PlainText: "My Database",
			},
		},
		Description: []notion.RichText{
			{
				Type:      "text",
				PlainText: "This is a test database",
			},
		},
	}

	// Create database pages with proper JSON properties
	titleProp1, err := json.Marshal(map[string]any{
		"type": "title",
		"title": []notion.RichText{
			{Type: "text", PlainText: "Child Page 1"},
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal title property: %v", err)
	}
	titleProp2, err := json.Marshal(map[string]any{
		"type": "title",
		"title": []notion.RichText{
			{Type: "text", PlainText: "Child Page 2"},
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal title property: %v", err)
	}

	dbPages := []notion.DatabasePage{
		{
			ID: "page1",
			Parent: notion.Parent{
				Type:       "database_id",
				DatabaseID: "db123e4567-e89b-12d3-a456-426614174000",
			},
			Properties: map[string]json.RawMessage{
				"title": titleProp1,
			},
		},
		{
			ID: "page2",
			Parent: notion.Parent{
				Type:       "database_id",
				DatabaseID: "db123e4567-e89b-12d3-a456-426614174000",
			},
			Properties: map[string]json.RawMessage{
				"title": titleProp2,
			},
		},
	}
	opts := ConvertOptions{
		FilePath: "tech/my-database.md",
	}

	result := c.ConvertDatabase(database, dbPages, opts)
	resultStr := string(result)

	// Check database title
	if !strings.Contains(resultStr, "# My Database") {
		t.Error("ConvertDatabase() should include database title")
	}

	// Check description
	if !strings.Contains(resultStr, "This is a test database") {
		t.Error("ConvertDatabase() should include database description")
	}

	// Check child page links
	if !strings.Contains(resultStr, "Child Page 1") {
		t.Error("ConvertDatabase() should include Child Page 1")
	}
	if !strings.Contains(resultStr, "Child Page 2") {
		t.Error("ConvertDatabase() should include Child Page 2")
	}

	// Check relative links are formed correctly
	if !strings.Contains(resultStr, "./my-database/child-page-1.md") {
		t.Error("ConvertDatabase() should generate correct relative link for child page 1")
	}
	if !strings.Contains(resultStr, "./my-database/child-page-2.md") {
		t.Error("ConvertDatabase() should generate correct relative link for child page 2")
	}
}

func TestConvertDatabase_NoChildren(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	database := &notion.Database{
		ID:             "db123",
		LastEditedTime: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		URL:            "https://notion.so/testdb",
		Title: []notion.RichText{
			{
				Type:      "text",
				PlainText: "Empty Database",
			},
		},
	}
	dbPages := []notion.DatabasePage{}
	opts := ConvertOptions{
		FilePath: "tech/empty-database.md",
	}

	result := c.ConvertDatabase(database, dbPages, opts)
	resultStr := string(result)

	// Check for empty message
	if !strings.Contains(resultStr, "*This database has no direct child pages.*") {
		t.Error("ConvertDatabase() should show empty message when no children")
	}
}

func TestConvertBlock_Paragraph(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	block := &notion.Block{
		Type: "paragraph",
		Paragraph: &notion.ParagraphBlock{
			RichText: []notion.RichText{
				{
					Type:      "text",
					PlainText: "Test paragraph",
				},
			},
		},
		Children: []notion.Block{
			{
				Type: "paragraph",
				Paragraph: &notion.ParagraphBlock{
					RichText: []notion.RichText{
						{
							Type:      "text",
							PlainText: "Nested paragraph",
						},
					},
				},
			},
		},
	}

	result := c.convertBlock(block, 0, ConvertOptions{})

	if !strings.Contains(result, "Test paragraph") {
		t.Error("convertBlock() should include paragraph text")
	}
	if !strings.Contains(result, "Nested paragraph") {
		t.Error("convertBlock() should include nested paragraph")
	}
}

func TestConvertBlock_Headings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		block      notion.Block
		wantPrefix string
	}{
		{
			name: "heading_1",
			block: notion.Block{
				Type: "heading_1",
				Heading1: &notion.HeadingBlock{
					RichText: []notion.RichText{
						{Type: "text", PlainText: "Heading 1"},
					},
				},
			},
			wantPrefix: "# Heading 1",
		},
		{
			name: "heading_2",
			block: notion.Block{
				Type: "heading_2",
				Heading2: &notion.HeadingBlock{
					RichText: []notion.RichText{
						{Type: "text", PlainText: "Heading 2"},
					},
				},
			},
			wantPrefix: "## Heading 2",
		},
		{
			name: "heading_3",
			block: notion.Block{
				Type: "heading_3",
				Heading3: &notion.HeadingBlock{
					RichText: []notion.RichText{
						{Type: "text", PlainText: "Heading 3"},
					},
				},
			},
			wantPrefix: "### Heading 3",
		},
	}

	c := NewConverter()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := c.convertBlock(&tt.block, 0, ConvertOptions{})
			if !strings.Contains(result, tt.wantPrefix) {
				t.Errorf("convertBlock() = %q, want to contain %q", result, tt.wantPrefix)
			}
		})
	}
}

func TestConvertBlock_HeadingsToggleable(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	block := &notion.Block{
		Type: "heading_2",
		Heading2: &notion.HeadingBlock{
			RichText: []notion.RichText{
				{Type: "text", PlainText: "Toggle Heading"},
			},
			IsToggleable: true,
		},
		Children: []notion.Block{
			{
				Type: "paragraph",
				Paragraph: &notion.ParagraphBlock{
					RichText: []notion.RichText{
						{Type: "text", PlainText: "Hidden content"},
					},
				},
			},
		},
	}

	result := c.convertBlock(block, 0, ConvertOptions{})

	if !strings.Contains(result, "## Toggle Heading") {
		t.Error("convertBlock() should include heading text")
	}
	if !strings.Contains(result, "<!-- collapsible: start -->") {
		t.Error("convertBlock() should include collapsible start marker")
	}
	if !strings.Contains(result, "<!-- collapsible: end -->") {
		t.Error("convertBlock() should include collapsible end marker")
	}
	if !strings.Contains(result, "Hidden content") {
		t.Error("convertBlock() should include children content")
	}
}

func TestConvertBlock_Lists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		block      notion.Block
		wantPrefix string
	}{
		{
			name: "bulleted_list_item",
			block: notion.Block{
				Type: "bulleted_list_item",
				BulletedListItem: &notion.ListItemBlock{
					RichText: []notion.RichText{
						{Type: "text", PlainText: "Bullet item"},
					},
				},
			},
			wantPrefix: "- Bullet item",
		},
		{
			name: "numbered_list_item",
			block: notion.Block{
				Type: "numbered_list_item",
				NumberedListItem: &notion.ListItemBlock{
					RichText: []notion.RichText{
						{Type: "text", PlainText: "Numbered item"},
					},
				},
			},
			wantPrefix: "1. Numbered item",
		},
		{
			name: "to_do_unchecked",
			block: notion.Block{
				Type: "to_do",
				ToDo: &notion.ToDoBlock{
					RichText: []notion.RichText{
						{Type: "text", PlainText: "Todo item"},
					},
					Checked: false,
				},
			},
			wantPrefix: "- [ ] Todo item",
		},
		{
			name: "to_do_checked",
			block: notion.Block{
				Type: "to_do",
				ToDo: &notion.ToDoBlock{
					RichText: []notion.RichText{
						{Type: "text", PlainText: "Done item"},
					},
					Checked: true,
				},
			},
			wantPrefix: "- [x] Done item",
		},
	}

	c := NewConverter()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := c.convertBlock(&tt.block, 0, ConvertOptions{})
			if !strings.Contains(result, tt.wantPrefix) {
				t.Errorf("convertBlock() = %q, want to contain %q", result, tt.wantPrefix)
			}
		})
	}
}

func TestConvertBlock_Code(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	block := &notion.Block{
		Type: "code",
		Code: &notion.CodeBlock{
			RichText: []notion.RichText{
				{Type: "text", PlainText: "func main() {}"},
			},
			Language: "go",
		},
	}

	result := c.convertBlock(block, 0, ConvertOptions{})

	if !strings.Contains(result, "```go") {
		t.Error("convertBlock() should include language in code fence")
	}
	if !strings.Contains(result, "func main() {}") {
		t.Error("convertBlock() should include code content")
	}
	if !strings.Contains(result, "```") {
		t.Error("convertBlock() should include closing code fence")
	}
}

func TestConvertBlock_Quote(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	block := &notion.Block{
		Type: "quote",
		Quote: &notion.QuoteBlock{
			RichText: []notion.RichText{
				{Type: "text", PlainText: "This is a quote"},
			},
		},
	}

	result := c.convertBlock(block, 0, ConvertOptions{})

	if !strings.Contains(result, "> This is a quote") {
		t.Error("convertBlock() should format quote with > prefix")
	}
}

func TestConvertBlock_Callout(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	block := &notion.Block{
		Type: "callout",
		Callout: &notion.CalloutBlock{
			RichText: []notion.RichText{
				{Type: "text", PlainText: "Important note"},
			},
			Icon: &notion.Icon{
				Type:  "emoji",
				Emoji: "💡",
			},
		},
	}

	result := c.convertBlock(block, 0, ConvertOptions{})

	if !strings.Contains(result, "💡") {
		t.Error("convertBlock() should include callout emoji")
	}
	if !strings.Contains(result, "> 💡 Important note") {
		t.Error("convertBlock() should format callout with emoji and text")
	}
}

func TestConvertBlock_Image(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	block := &notion.Block{
		ID:   "img123",
		Type: "image",
		Image: &notion.FileBlock{
			File: &notion.File{
				URL: "https://example.com/image.png",
			},
			Caption: []notion.RichText{
				{Type: "text", PlainText: "My Image"},
			},
		},
	}

	// Test without file processor
	result := c.convertBlock(block, 0, ConvertOptions{})
	if !strings.Contains(result, "![My Image](https://example.com/image.png)") {
		t.Error("convertBlock() should format image with caption and URL")
	}
	if !strings.Contains(result, "<!-- file_id:img123 -->") {
		t.Error("convertBlock() should include file_id comment")
	}

	// Test with file processor
	fileProcessor := func(_ string) string {
		return "./files/image.png"
	}
	opts := ConvertOptions{FileProcessor: fileProcessor}
	result = c.convertBlock(block, 0, opts)
	if !strings.Contains(result, "![My Image](./files/image.png)") {
		t.Error("convertBlock() should use file processor to transform URL")
	}
}

func TestConvertBlock_ChildPage(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	block := &notion.Block{
		ID:   "child123",
		Type: "child_page",
		ChildPage: &notion.ChildPageBlock{
			Title: "Child Page Title",
		},
	}
	opts := ConvertOptions{
		PageTitle: "Parent Page",
	}

	result := c.convertBlock(block, 0, opts)

	if !strings.Contains(result, "[Child Page Title]") {
		t.Error("convertBlock() should include child page title as link text")
	}
	if !strings.Contains(result, "./parent-page/child-page-title.md") {
		t.Error("convertBlock() should generate correct relative path")
	}
	if !strings.Contains(result, "<!-- page_id:child123 -->") {
		t.Error("convertBlock() should include page_id comment")
	}
}

func TestConvertBlock_Table(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	block := &notion.Block{
		Type: "table",
		Table: &notion.TableBlock{
			TableWidth:      2,
			HasColumnHeader: true,
		},
		Children: []notion.Block{
			{
				Type: "table_row",
				TableRow: &notion.TableRowBlock{
					Cells: [][]notion.RichText{
						{{Type: "text", PlainText: "Header 1"}},
						{{Type: "text", PlainText: "Header 2"}},
					},
				},
			},
			{
				Type: "table_row",
				TableRow: &notion.TableRowBlock{
					Cells: [][]notion.RichText{
						{{Type: "text", PlainText: "Cell 1"}},
						{{Type: "text", PlainText: "Cell 2"}},
					},
				},
			},
		},
	}

	result := c.convertBlock(block, 0, ConvertOptions{})

	// Check table structure
	if !strings.Contains(result, "| Header 1 | Header 2 |") {
		t.Error("convertBlock() should include table headers")
	}
	if !strings.Contains(result, "| --- | --- |") {
		t.Error("convertBlock() should include header separator")
	}
	if !strings.Contains(result, "| Cell 1 | Cell 2 |") {
		t.Error("convertBlock() should include table cells")
	}
}

func TestConvertBlock_Divider(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	block := &notion.Block{
		Type: "divider",
	}

	result := c.convertBlock(block, 0, ConvertOptions{})

	if result != "---\n" {
		t.Errorf("convertBlock() = %q, want %q", result, "---\n")
	}
}

func TestConvertBlock_Unknown(t *testing.T) {
	t.Parallel()

	c := NewConverter()
	block := &notion.Block{
		Type: "unknown_block_type",
	}

	result := c.convertBlock(block, 0, ConvertOptions{})

	if result != "" {
		t.Errorf("convertBlock() should return empty string for unknown block type, got %q", result)
	}
}
