// Package converter provides functionality to convert Notion pages and blocks to Markdown format.
package converter

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fclairamb/ntnsync/internal/notion"
)

const (
	blockTypeFile      = "file"
	defaultUntitledStr = "untitled"
)

// Converter converts Notion pages and blocks to Markdown.
type Converter struct {
	// IncludeFrontmatter controls whether to include YAML frontmatter.
	IncludeFrontmatter bool
}

// FileProcessor processes a file URL and returns the local path.
// If the file should be downloaded, it downloads it and returns the local path.
// If nil, files are not processed and URLs are used as-is.
type FileProcessor func(fileURL string) string

// ConvertOptions contains additional metadata for conversion.
type ConvertOptions struct {
	Folder        string        // Folder name for this page
	PageTitle     string        // Page title (used for child page link paths)
	FilePath      string        // File path (stored in frontmatter)
	LastSynced    time.Time     // When we synced this page
	NotionType    string        // Type: "page" or "database"
	IsRoot        bool          // Whether this is a root page
	ParentID      string        // Resolved parent page/database ID (empty for root pages)
	FileProcessor FileProcessor // Optional callback to process file URLs
}

// NewConverter creates a new converter with default settings.
func NewConverter() *Converter {
	return &Converter{
		IncludeFrontmatter: true,
	}
}

// Convert converts a page and its blocks to Markdown.
func (c *Converter) Convert(page *notion.Page, blocks []notion.Block) []byte {
	return c.ConvertWithOptions(page, blocks, ConvertOptions{})
}

// ConvertWithOptions converts a page and its blocks to Markdown with additional options.
func (c *Converter) ConvertWithOptions(page *notion.Page, blocks []notion.Block, opts ConvertOptions) []byte {
	var builder strings.Builder

	if c.IncludeFrontmatter {
		builder.WriteString(c.generateFrontmatter(page, opts))
	}

	// Add title as h1
	title := page.Title()
	if title != "" {
		builder.WriteString(fmt.Sprintf("# %s\n\n", title))
	}

	// Convert blocks
	for i := range blocks {
		block := &blocks[i]
		content := c.convertBlock(block, 0, opts)
		builder.WriteString(content)

		// Add spacing between blocks (but not after last block)
		if i < len(blocks)-1 && content != "" {
			// Don't add extra newline after list items if next is also a list item
			if !c.isListItem(block) || !c.isListItem(&blocks[i+1]) {
				builder.WriteString("\n")
			}
		}
	}

	return []byte(builder.String())
}

// ConvertDatabase converts a database to Markdown with a list of direct child pages.
func (c *Converter) ConvertDatabase(
	database *notion.Database, dbPages []notion.DatabasePage, opts ConvertOptions,
) []byte {
	var builder strings.Builder

	if c.IncludeFrontmatter {
		// Create a pseudo-page for frontmatter generation
		page := &notion.Page{
			ID:             database.ID,
			LastEditedTime: database.LastEditedTime,
			Parent:         database.Parent,
			URL:            database.URL,
		}
		builder.WriteString(c.generateFrontmatter(page, opts))
	}

	// Add database title as heading
	title := database.GetTitle()
	if title != "" {
		builder.WriteString(fmt.Sprintf("# %s\n\n", title))
	}

	// Add description if present
	description := notion.ParseRichText(database.Description)
	if description != "" {
		builder.WriteString(description + "\n\n")
	}

	// Normalize database ID for comparison
	dbID := strings.ReplaceAll(database.ID, "-", "")

	// Filter to only show direct child pages (parent is this database)
	var directChildren []notion.DatabasePage
	for i := range dbPages {
		dbPage := &dbPages[i]
		if parentID := dbPage.Parent.ID(); parentID != "" {
			pageParentDBID := strings.ReplaceAll(parentID, "-", "")
			if pageParentDBID == dbID {
				directChildren = append(directChildren, *dbPage)
			}
		}
	}

	// Add list with links to direct child pages
	if len(directChildren) > 0 {
		// Extract the base filename from file path to use for links
		// This ensures we use the sanitized filename (e.g., "wiki" not "Wiki")
		baseFilename := strings.TrimSuffix(filepath.Base(opts.FilePath), ".md")

		for i := range directChildren {
			dbPage := &directChildren[i]
			pageTitle := dbPage.Title()
			if pageTitle == "" {
				pageTitle = "Untitled"
			}

			// Generate relative link to the page
			// Use sanitized base filename from file path, not original title
			slug := SanitizeFilename(pageTitle)
			relPath := fmt.Sprintf("./%s/%s.md", baseFilename, slug)
			pageID := NormalizeID(dbPage.ID)

			builder.WriteString(fmt.Sprintf("- [%s](%s)<!-- page_id:%s -->\n", pageTitle, relPath, pageID))
		}
		builder.WriteString("\n")
	} else {
		builder.WriteString("*This database has no direct child pages.*\n\n")
	}

	return []byte(builder.String())
}

// generateFrontmatter creates YAML frontmatter for the page.
func (c *Converter) generateFrontmatter(page *notion.Page, opts ConvertOptions) string {
	var builder strings.Builder
	builder.WriteString("---\n")
	builder.WriteString(fmt.Sprintf("notion_id: %s\n", page.ID))

	// Title (use page title, or opts.PageTitle for databases)
	title := page.Title()
	if title == "" {
		title = opts.PageTitle
	}
	if title != "" {
		builder.WriteString(fmt.Sprintf("title: %q\n", title))
	}

	// Notion type (page or database)
	notionType := opts.NotionType
	if notionType == "" {
		notionType = "page"
	}
	builder.WriteString(fmt.Sprintf("notion_type: %s\n", notionType))

	// Use provided folder
	if opts.Folder != "" {
		builder.WriteString(fmt.Sprintf("notion_folder: %s\n", opts.Folder))
	}

	// File path for self-reference
	if opts.FilePath != "" {
		builder.WriteString(fmt.Sprintf("file_path: %s\n", opts.FilePath))
	}

	builder.WriteString(fmt.Sprintf("last_edited: %s\n", page.LastEditedTime.Format(time.RFC3339)))

	// Last synced time
	if !opts.LastSynced.IsZero() {
		builder.WriteString(fmt.Sprintf("last_synced: %s\n", opts.LastSynced.Format(time.RFC3339)))
	}

	// Include resolved parent ID (page or database, never block)
	if opts.ParentID != "" {
		builder.WriteString(fmt.Sprintf("notion_parent_id: %s\n", opts.ParentID))
	}

	builder.WriteString(fmt.Sprintf("is_root: %t\n", opts.IsRoot))
	builder.WriteString(fmt.Sprintf("notion_url: %s\n", page.URL))
	builder.WriteString("---\n\n")
	return builder.String()
}

// convertBlock converts a single block to Markdown.
//
//nolint:funlen,gocognit // Large switch statement for all Notion block types
func (c *Converter) convertBlock(block *notion.Block, depth int, opts ConvertOptions) string {
	indent := strings.Repeat("  ", depth)

	switch block.Type {
	case "paragraph":
		if block.Paragraph == nil {
			return "\n"
		}
		text := notion.ParseRichTextToMarkdown(block.Paragraph.RichText)
		if text == "" {
			return "\n"
		}
		result := text + "\n"
		result += c.convertChildren(block.Children, depth, opts)
		return result

	case "heading_1":
		if block.Heading1 == nil {
			return ""
		}
		text := notion.ParseRichTextToMarkdown(block.Heading1.RichText)
		if block.Heading1.IsToggleable {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("# %s\n", text))
			sb.WriteString("<!-- collapsible: start -->\n")
			sb.WriteString(c.convertChildren(block.Children, 0, opts))
			sb.WriteString("<!-- collapsible: end -->\n")
			return sb.String()
		}
		return fmt.Sprintf("# %s\n", text)

	case "heading_2":
		if block.Heading2 == nil {
			return ""
		}
		text := notion.ParseRichTextToMarkdown(block.Heading2.RichText)
		if block.Heading2.IsToggleable {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("## %s\n", text))
			sb.WriteString("<!-- collapsible: start -->\n")
			sb.WriteString(c.convertChildren(block.Children, 0, opts))
			sb.WriteString("<!-- collapsible: end -->\n")
			return sb.String()
		}
		return fmt.Sprintf("## %s\n", text)

	case "heading_3":
		if block.Heading3 == nil {
			return ""
		}
		text := notion.ParseRichTextToMarkdown(block.Heading3.RichText)
		if block.Heading3.IsToggleable {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("### %s\n", text))
			sb.WriteString("<!-- collapsible: start -->\n")
			sb.WriteString(c.convertChildren(block.Children, 0, opts))
			sb.WriteString("<!-- collapsible: end -->\n")
			return sb.String()
		}
		return fmt.Sprintf("### %s\n", text)

	case "bulleted_list_item":
		if block.BulletedListItem == nil {
			return ""
		}
		text := notion.ParseRichTextToMarkdown(block.BulletedListItem.RichText)
		result := fmt.Sprintf("%s- %s\n", indent, text)
		result += c.convertChildren(block.Children, depth+1, opts)
		return result

	case "numbered_list_item":
		if block.NumberedListItem == nil {
			return ""
		}
		text := notion.ParseRichTextToMarkdown(block.NumberedListItem.RichText)
		result := fmt.Sprintf("%s1. %s\n", indent, text)
		result += c.convertChildren(block.Children, depth+1, opts)
		return result

	case "to_do":
		if block.ToDo == nil {
			return ""
		}
		text := notion.ParseRichTextToMarkdown(block.ToDo.RichText)
		checkbox := "[ ]"
		if block.ToDo.Checked {
			checkbox = "[x]"
		}
		result := fmt.Sprintf("%s- %s %s\n", indent, checkbox, text)
		result += c.convertChildren(block.Children, depth+1, opts)
		return result

	case "toggle":
		if block.Toggle == nil {
			return ""
		}
		text := notion.ParseRichTextToMarkdown(block.Toggle.RichText)
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("<!-- collapsible: start -->\n**%s**\n\n", text))
		sb.WriteString(c.convertChildren(block.Children, 0, opts))
		sb.WriteString("<!-- collapsible: end -->\n")
		return sb.String()

	case "code":
		if block.Code == nil {
			return ""
		}
		text := notion.ParseRichText(block.Code.RichText) // No markdown formatting inside code
		lang := block.Code.Language
		if lang == "plain text" {
			lang = ""
		}
		return fmt.Sprintf("```%s\n%s\n```\n", lang, text)

	case "quote":
		if block.Quote == nil {
			return ""
		}
		text := notion.ParseRichTextToMarkdown(block.Quote.RichText)
		lines := strings.Split(text, "\n")
		var sb strings.Builder
		for _, line := range lines {
			sb.WriteString(fmt.Sprintf("> %s\n", line))
		}
		sb.WriteString(c.convertChildren(block.Children, depth, opts))
		return sb.String()

	case "callout":
		if block.Callout == nil {
			return ""
		}
		text := notion.ParseRichTextToMarkdown(block.Callout.RichText)
		emoji := ""
		if block.Callout.Icon != nil && block.Callout.Icon.Emoji != "" {
			emoji = block.Callout.Icon.Emoji + " "
		}
		lines := strings.Split(text, "\n")
		var builder strings.Builder
		for i, line := range lines {
			prefix := "> "
			if i == 0 {
				prefix = "> " + emoji
			}
			builder.WriteString(fmt.Sprintf("%s%s\n", prefix, line))
		}
		builder.WriteString(c.convertChildren(block.Children, depth, opts))
		return builder.String()

	case "divider":
		return "---\n"

	case "image":
		if block.Image == nil {
			return ""
		}
		fileURL := c.getFileURL(block.Image)
		if opts.FileProcessor != nil {
			fileURL = opts.FileProcessor(fileURL)
		}
		caption := notion.ParseRichText(block.Image.Caption)
		if caption == "" {
			caption = "image"
		}
		fileID := NormalizeID(block.ID)
		return fmt.Sprintf("![%s](%s)<!-- file_id:%s -->\n", caption, fileURL, fileID)

	case "video":
		if block.Video == nil {
			return ""
		}
		fileURL := c.getFileURL(block.Video)
		if opts.FileProcessor != nil {
			fileURL = opts.FileProcessor(fileURL)
		}
		caption := notion.ParseRichText(block.Video.Caption)
		if caption == "" {
			caption = "Video"
		}
		fileID := NormalizeID(block.ID)
		return fmt.Sprintf("[%s](%s)<!-- file_id:%s -->\n", caption, fileURL, fileID)

	case blockTypeFile:
		if block.File == nil {
			return ""
		}
		fileURL := c.getFileURL(block.File)
		if opts.FileProcessor != nil {
			fileURL = opts.FileProcessor(fileURL)
		}
		name := block.File.Name
		if name == "" {
			name = "File"
		}
		fileID := NormalizeID(block.ID)
		return fmt.Sprintf("[%s](%s)<!-- file_id:%s -->\n", name, fileURL, fileID)

	case "pdf":
		if block.PDF == nil {
			return ""
		}
		fileURL := c.getFileURL(block.PDF)
		if opts.FileProcessor != nil {
			fileURL = opts.FileProcessor(fileURL)
		}
		caption := notion.ParseRichText(block.PDF.Caption)
		if caption == "" {
			caption = "PDF"
		}
		fileID := NormalizeID(block.ID)
		return fmt.Sprintf("[%s](%s)<!-- file_id:%s -->\n", caption, fileURL, fileID)

	case "bookmark":
		if block.Bookmark == nil {
			return ""
		}
		caption := notion.ParseRichText(block.Bookmark.Caption)
		if caption == "" {
			caption = block.Bookmark.URL
		}
		return fmt.Sprintf("[%s](%s)\n", caption, block.Bookmark.URL)

	case "equation":
		if block.Equation == nil {
			return ""
		}
		return fmt.Sprintf("$$\n%s\n$$\n", block.Equation.Expression)

	case "table_of_contents":
		return "[TOC]\n"

	case "child_page":
		if block.ChildPage == nil {
			return ""
		}
		// Link to child page - uses parent page's title as directory name
		parentDir := strings.ToLower(SanitizeFilename(opts.PageTitle))
		childFile := strings.ToLower(SanitizeFilename(block.ChildPage.Title))
		pageID := NormalizeID(block.ID)
		return fmt.Sprintf("- [%s](./%s/%s.md)<!-- page_id:%s -->\n", block.ChildPage.Title, parentDir, childFile, pageID)

	case "child_database":
		if block.ChildDatabase == nil {
			return ""
		}
		// Link to child database - uses parent page's title as directory name
		parentDir := strings.ToLower(SanitizeFilename(opts.PageTitle))
		childFile := strings.ToLower(SanitizeFilename(block.ChildDatabase.Title))
		dbID := NormalizeID(block.ID)
		return fmt.Sprintf("- [%s](./%s/%s.md)<!-- page_id:%s -->\n", block.ChildDatabase.Title, parentDir, childFile, dbID)

	case "synced_block":
		// Just render children for synced blocks
		return c.convertChildren(block.Children, depth, opts)

	case "table":
		if block.Table == nil {
			return ""
		}
		return c.convertTable(block)

	case "column_list":
		// Render columns sequentially
		return c.convertChildren(block.Children, depth, opts)

	case "column":
		// Render column content
		return c.convertChildren(block.Children, depth, opts)

	case "link_to_page":
		if block.LinkToPage == nil {
			return ""
		}
		if block.LinkToPage.PageID != "" {
			pageID := NormalizeID(block.LinkToPage.PageID)
			return fmt.Sprintf("[Page Link](notion://page/%s)<!-- page_id:%s -->\n", block.LinkToPage.PageID, pageID)
		}
		if block.LinkToPage.DatabaseID != "" {
			dbID := NormalizeID(block.LinkToPage.DatabaseID)
			return fmt.Sprintf("[Database Link](notion://database/%s)<!-- page_id:%s -->\n", block.LinkToPage.DatabaseID, dbID)
		}
		return ""

	case "embed":
		if block.Embed == nil {
			return ""
		}
		return fmt.Sprintf("[Embed](%s)\n", block.Embed.URL)

	default:
		// Unknown block type - skip
		return ""
	}
}

// convertChildren converts child blocks.
func (c *Converter) convertChildren(children []notion.Block, depth int, opts ConvertOptions) string {
	var sb strings.Builder
	for i := range children {
		sb.WriteString(c.convertBlock(&children[i], depth, opts))
	}
	return sb.String()
}

// convertTable converts a table block with its rows.
func (c *Converter) convertTable(block *notion.Block) string {
	if block.Table == nil || len(block.Children) == 0 {
		return ""
	}

	var builder strings.Builder
	width := block.Table.TableWidth

	for i := range block.Children {
		row := &block.Children[i]
		if row.TableRow == nil {
			continue
		}

		// Build row
		builder.WriteString("|")
		for j := range width {
			cell := ""
			if j < len(row.TableRow.Cells) {
				cell = notion.ParseRichTextToMarkdown(row.TableRow.Cells[j])
			}
			builder.WriteString(fmt.Sprintf(" %s |", cell))
		}
		builder.WriteString("\n")

		// Add header separator after first row if it's a header
		if i == 0 && block.Table.HasColumnHeader {
			builder.WriteString("|")
			for range width {
				builder.WriteString(" --- |")
			}
			builder.WriteString("\n")
		}
	}

	return builder.String()
}

// getFileURL extracts URL from a file block.
func (c *Converter) getFileURL(file *notion.FileBlock) string {
	if file == nil {
		return ""
	}
	if file.External != nil {
		return file.External.URL
	}
	if file.File != nil {
		return file.File.URL
	}
	return ""
}

// isListItem checks if a block is a list item.
func (c *Converter) isListItem(block *notion.Block) bool {
	return block.Type == "bulleted_list_item" ||
		block.Type == "numbered_list_item" ||
		block.Type == "to_do"
}
