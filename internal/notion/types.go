// Package notion provides a client for the Notion API.
package notion

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Page represents a Notion page.
type Page struct {
	Object         string     `json:"object"`
	ID             string     `json:"id"`
	CreatedTime    time.Time  `json:"created_time"`
	LastEditedTime time.Time  `json:"last_edited_time"`
	CreatedBy      User       `json:"created_by"`
	LastEditedBy   User       `json:"last_edited_by"`
	Parent         Parent     `json:"parent"`
	Archived       bool       `json:"archived"`
	InTrash        bool       `json:"in_trash"`
	Icon           *Icon      `json:"icon"`
	Cover          *FileBlock `json:"cover"`
	Properties     Properties `json:"properties"`
	URL            string     `json:"url"`
	PublicURL      *string    `json:"public_url"`
}

// Database represents a Notion database.
// In API version 2025-09-03, this is populated from both the database container
// and the first data source.
type Database struct {
	Object         string         `json:"object"`
	ID             string         `json:"id"`
	CreatedTime    time.Time      `json:"created_time"`
	LastEditedTime time.Time      `json:"last_edited_time"`
	CreatedBy      User           `json:"created_by"`
	LastEditedBy   User           `json:"last_edited_by"`
	Title          []RichText     `json:"title"`
	Description    []RichText     `json:"description"`
	Icon           *Icon          `json:"icon"`
	Cover          *FileBlock     `json:"cover"`
	Properties     map[string]any `json:"properties"`
	Parent         Parent         `json:"parent"`
	URL            string         `json:"url"`
	PublicURL      *string        `json:"public_url"`
	Archived       bool           `json:"archived"`
	InTrash        bool           `json:"in_trash"`
	IsInline       bool           `json:"is_inline"`
	// DataSourceID is the ID of the primary data source (API 2025-09-03+).
	DataSourceID string `json:"data_source_id,omitempty"`
	// DataSources contains all data sources in this database (API 2025-09-03+).
	DataSources []DataSourceInfo `json:"data_sources,omitempty"`
}

// DataSourceInfo represents basic info about a data source in a database container.
type DataSourceInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DataSource represents a full data source with schema (API 2025-09-03+).
type DataSource struct {
	Object         string         `json:"object"` // "data_source"
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	CreatedTime    time.Time      `json:"created_time"`
	LastEditedTime time.Time      `json:"last_edited_time"`
	CreatedBy      User           `json:"created_by"`
	LastEditedBy   User           `json:"last_edited_by"`
	Title          []RichText     `json:"title"`
	Description    []RichText     `json:"description"`
	Properties     map[string]any `json:"properties"`
	Parent         Parent         `json:"parent"`
	URL            string         `json:"url"`
	Archived       bool           `json:"archived"`
	InTrash        bool           `json:"in_trash"`
}

// DatabaseContainer represents the database container response (API 2025-09-03+).
// GET /databases/{id} now returns this instead of the schema.
type DatabaseContainer struct {
	Object         string           `json:"object"` // "database"
	ID             string           `json:"id"`
	CreatedTime    time.Time        `json:"created_time"`
	LastEditedTime time.Time        `json:"last_edited_time"`
	CreatedBy      User             `json:"created_by"`
	LastEditedBy   User             `json:"last_edited_by"`
	DataSources    []DataSourceInfo `json:"data_sources"`
	Title          []RichText       `json:"title"`
	Description    []RichText       `json:"description"`
	Icon           *Icon            `json:"icon"`
	Cover          *FileBlock       `json:"cover"`
	Parent         Parent           `json:"parent"`
	URL            string           `json:"url"`
	PublicURL      *string          `json:"public_url"`
	IsInline       bool             `json:"is_inline"`
	Archived       bool             `json:"archived"`
	InTrash        bool             `json:"in_trash"`
}

// GetTitle returns the database title as a string.
func (d *Database) GetTitle() string {
	return ParseRichText(d.Title)
}

// QueryDatabaseResponse represents the response from querying a database.
type QueryDatabaseResponse struct {
	Object     string         `json:"object"`
	Results    []DatabasePage `json:"results"`
	NextCursor *string        `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
	Type       string         `json:"type"`
}

// DatabasePage represents a page returned from a database query.
// It has a simpler structure than Page to handle the complex property types.
type DatabasePage struct {
	Object         string                     `json:"object"`
	ID             string                     `json:"id"`
	CreatedTime    time.Time                  `json:"created_time"`
	LastEditedTime time.Time                  `json:"last_edited_time"`
	CreatedBy      User                       `json:"created_by"`
	LastEditedBy   User                       `json:"last_edited_by"`
	Parent         Parent                     `json:"parent"`
	Archived       bool                       `json:"archived"`
	InTrash        bool                       `json:"in_trash"`
	Icon           *Icon                      `json:"icon"`
	Cover          *FileBlock                 `json:"cover"`
	Properties     map[string]json.RawMessage `json:"properties"`
	URL            string                     `json:"url"`
	PublicURL      *string                    `json:"public_url"`
}

// Title extracts the title from database page properties.
func (p *DatabasePage) Title() string {
	// Try to find a title property
	for _, propData := range p.Properties {
		var prop struct {
			Type  string     `json:"type"`
			Title []RichText `json:"title,omitempty"`
		}
		if err := json.Unmarshal(propData, &prop); err != nil {
			continue
		}
		if prop.Type == "title" && len(prop.Title) > 0 {
			return ParseRichText(prop.Title)
		}
	}
	return "Untitled"
}

// ToPage converts a DatabasePage to a regular Page.
func (p *DatabasePage) ToPage() *Page {
	return &Page{
		Object:         p.Object,
		ID:             p.ID,
		CreatedTime:    p.CreatedTime,
		LastEditedTime: p.LastEditedTime,
		CreatedBy:      p.CreatedBy,
		LastEditedBy:   p.LastEditedBy,
		Parent:         p.Parent,
		Archived:       p.Archived,
		InTrash:        p.InTrash,
		Icon:           p.Icon,
		Cover:          p.Cover,
		Properties:     Properties{}, // Empty - we extract title separately
		URL:            p.URL,
		PublicURL:      p.PublicURL,
	}
}

// Title extracts the title from page properties.
func (p *Page) Title() string {
	if title, ok := p.Properties["title"]; ok {
		return ParseRichText(title.Title)
	}
	if title, ok := p.Properties["Name"]; ok {
		return ParseRichText(title.Title)
	}
	// Try to find any title property
	for key := range p.Properties {
		prop := p.Properties[key]
		if prop.Type == "title" && len(prop.Title) > 0 {
			return ParseRichText(prop.Title)
		}
	}
	return "Untitled"
}

// User represents a Notion user reference.
type User struct {
	Object    string   `json:"object"`
	ID        string   `json:"id"`
	Type      string   `json:"type,omitempty"`
	Name      string   `json:"name,omitempty"`
	AvatarURL *string  `json:"avatar_url,omitempty"`
	Person    *Person  `json:"person,omitempty"`
	Bot       *BotInfo `json:"bot,omitempty"`
}

// Person contains person-specific user data.
type Person struct {
	Email string `json:"email"`
}

// BotInfo contains bot-specific user data within a User struct.
type BotInfo struct {
	Owner          *BotOwner `json:"owner,omitempty"`
	WorkspaceOwner string    `json:"workspace_owner,omitempty"`
}

// BotOwner represents the owner of a bot.
type BotOwner struct {
	Type string `json:"type"`
	User *User  `json:"user,omitempty"`
}

// shortIDLength is the number of characters to use for short user IDs.
const shortIDLength = 8

// Format returns the user in a human-readable format.
// Format: "Name <email> [short_id]"
// - Name defaults to "Unknown" if empty.
// - Email is omitted if not available (person without email, or bot).
// - Short ID is first 8 characters of the UUID.
func (u *User) Format() string {
	if u == nil {
		return ""
	}

	name := u.Name
	if name == "" {
		name = "Unknown"
	}

	// Short ID (first 8 chars)
	shortID := u.ID
	if len(shortID) > shortIDLength {
		shortID = shortID[:shortIDLength]
	}

	// Person with email: "Name <email> [id]"
	if u.Type == "person" && u.Person != nil && u.Person.Email != "" {
		return fmt.Sprintf("%s <%s> [%s]", name, u.Person.Email, shortID)
	}

	// No email available: "Name [id]"
	return fmt.Sprintf("%s [%s]", name, shortID)
}

// Parent represents the parent of a page or block.
type Parent struct {
	Type         string `json:"type"`
	PageID       string `json:"page_id,omitempty"`
	DatabaseID   string `json:"database_id,omitempty"`
	DataSourceID string `json:"data_source_id,omitempty"` // API 2025-09-03+
	BlockID      string `json:"block_id,omitempty"`
	Workspace    bool   `json:"workspace,omitempty"`
	SpaceID      string `json:"space_id,omitempty"` // For teamspaces
}

// IsWorkspaceLevel returns true if the parent is at workspace level (private or teamspace).
func (p *Parent) IsWorkspaceLevel() bool {
	return p.Type == "workspace" || p.Workspace || p.Type == "space"
}

// ID returns the parent ID regardless of type (page, database, block, or space).
func (p *Parent) ID() string {
	if p.PageID != "" {
		return p.PageID
	}
	if p.DatabaseID != "" {
		return p.DatabaseID
	}
	if p.BlockID != "" {
		return p.BlockID
	}
	if p.SpaceID != "" {
		return p.SpaceID
	}
	return ""
}

// Properties is a map of property name to property value.
type Properties map[string]Property

// Property represents a page property.
// This handles various property types from both regular pages and database pages.
type Property struct {
	ID   string `json:"id"`
	Type string `json:"type"`

	// Title property (for title type)
	Title []RichText `json:"title,omitempty"`

	// Rich text property
	RichText []RichText `json:"rich_text,omitempty"`

	// Number property
	Number *float64 `json:"number,omitempty"`

	// Select property
	Select *SelectOption `json:"select,omitempty"`

	// Multi-select property
	MultiSelect []SelectOption `json:"multi_select,omitempty"`

	// Status property
	Status *SelectOption `json:"status,omitempty"`

	// Date property
	Date *DateProperty `json:"date,omitempty"`

	// Checkbox property
	Checkbox bool `json:"checkbox,omitempty"`

	// URL property
	URL *string `json:"url,omitempty"`

	// Email property
	Email *string `json:"email,omitempty"`

	// Phone number property
	PhoneNumber *string `json:"phone_number,omitempty"`

	// Files property
	Files []FileObject `json:"files,omitempty"`

	// People property
	People []User `json:"people,omitempty"`

	// Relation property
	Relation []RelationItem `json:"relation,omitempty"`

	// Rollup property
	Rollup *RollupValue `json:"rollup,omitempty"`

	// Formula property
	Formula *FormulaValue `json:"formula,omitempty"`

	// Created by property
	CreatedBy *User `json:"created_by,omitempty"`

	// Last edited by property
	LastEditedBy *User `json:"last_edited_by,omitempty"`

	// Created time property
	CreatedTime *string `json:"created_time,omitempty"`

	// Last edited time property
	LastEditedTime *string `json:"last_edited_time,omitempty"`

	// Unique ID property
	UniqueID *UniqueIDValue `json:"unique_id,omitempty"`

	// Verification property
	Verification *VerificationValue `json:"verification,omitempty"`
}

// UniqueIDValue represents a unique ID property value.
type UniqueIDValue struct {
	Prefix *string `json:"prefix,omitempty"`
	Number int     `json:"number"`
}

// VerificationValue represents a verification property value.
type VerificationValue struct {
	State      string        `json:"state"`
	VerifiedBy *User         `json:"verified_by,omitempty"`
	Date       *DateProperty `json:"date,omitempty"`
}

// SelectOption represents a select/multi-select/status option.
type SelectOption struct {
	ID    string `json:"id,omitempty"`
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}

// DateProperty represents a date property value.
type DateProperty struct {
	Start    string  `json:"start"`
	End      *string `json:"end,omitempty"`
	TimeZone *string `json:"time_zone,omitempty"`
}

// FileObject represents a file in a files property.
type FileObject struct {
	Name     string        `json:"name"`
	Type     string        `json:"type"`
	File     *File         `json:"file,omitempty"`
	External *ExternalFile `json:"external,omitempty"`
}

// RelationItem represents an item in a relation property.
type RelationItem struct {
	ID string `json:"id"`
}

// RollupValue represents a rollup property value.
type RollupValue struct {
	Type   string        `json:"type"`
	Number *float64      `json:"number,omitempty"`
	Date   *DateProperty `json:"date,omitempty"`
	Array  []any         `json:"array,omitempty"`
}

// FormulaValue represents a formula property value.
type FormulaValue struct {
	Type    string        `json:"type"`
	String  *string       `json:"string,omitempty"`
	Number  *float64      `json:"number,omitempty"`
	Boolean *bool         `json:"boolean,omitempty"`
	Date    *DateProperty `json:"date,omitempty"`
}

// Block represents a Notion block.
type Block struct {
	Object         string    `json:"object"`
	ID             string    `json:"id"`
	Parent         Parent    `json:"parent"`
	Type           string    `json:"type"`
	CreatedTime    time.Time `json:"created_time"`
	LastEditedTime time.Time `json:"last_edited_time"`
	CreatedBy      User      `json:"created_by"`
	LastEditedBy   User      `json:"last_edited_by"`
	HasChildren    bool      `json:"has_children"`
	Archived       bool      `json:"archived"`
	InTrash        bool      `json:"in_trash"`

	// Block type specific content
	Paragraph        *ParagraphBlock       `json:"paragraph,omitempty"`
	Heading1         *HeadingBlock         `json:"heading_1,omitempty"`
	Heading2         *HeadingBlock         `json:"heading_2,omitempty"`
	Heading3         *HeadingBlock         `json:"heading_3,omitempty"`
	BulletedListItem *ListItemBlock        `json:"bulleted_list_item,omitempty"`
	NumberedListItem *ListItemBlock        `json:"numbered_list_item,omitempty"`
	ToDo             *ToDoBlock            `json:"to_do,omitempty"`
	Toggle           *ToggleBlock          `json:"toggle,omitempty"`
	Code             *CodeBlock            `json:"code,omitempty"`
	Quote            *QuoteBlock           `json:"quote,omitempty"`
	Callout          *CalloutBlock         `json:"callout,omitempty"`
	Divider          *DividerBlock         `json:"divider,omitempty"`
	Image            *FileBlock            `json:"image,omitempty"`
	Video            *FileBlock            `json:"video,omitempty"`
	File             *FileBlock            `json:"file,omitempty"`
	PDF              *FileBlock            `json:"pdf,omitempty"`
	Bookmark         *BookmarkBlock        `json:"bookmark,omitempty"`
	Equation         *EquationBlock        `json:"equation,omitempty"`
	TableOfContents  *TableOfContentsBlock `json:"table_of_contents,omitempty"`
	ChildPage        *ChildPageBlock       `json:"child_page,omitempty"`
	ChildDatabase    *ChildDatabaseBlock   `json:"child_database,omitempty"`
	SyncedBlock      *SyncedBlockBlock     `json:"synced_block,omitempty"`
	Table            *TableBlock           `json:"table,omitempty"`
	TableRow         *TableRowBlock        `json:"table_row,omitempty"`
	ColumnList       *ColumnListBlock      `json:"column_list,omitempty"`
	Column           *ColumnBlock          `json:"column,omitempty"`
	LinkToPage       *LinkToPageBlock      `json:"link_to_page,omitempty"`
	Embed            *EmbedBlock           `json:"embed,omitempty"`

	// Children holds nested blocks (populated by recursive fetch)
	Children []Block `json:"-"`
}

// ParagraphBlock contains paragraph content.
type ParagraphBlock struct {
	RichText []RichText `json:"rich_text"`
	Color    string     `json:"color"`
}

// HeadingBlock contains heading content.
type HeadingBlock struct {
	RichText     []RichText `json:"rich_text"`
	Color        string     `json:"color"`
	IsToggleable bool       `json:"is_toggleable"`
}

// ListItemBlock contains list item content.
type ListItemBlock struct {
	RichText []RichText `json:"rich_text"`
	Color    string     `json:"color"`
}

// ToDoBlock contains to-do content.
type ToDoBlock struct {
	RichText []RichText `json:"rich_text"`
	Checked  bool       `json:"checked"`
	Color    string     `json:"color"`
}

// ToggleBlock contains toggle content.
type ToggleBlock struct {
	RichText []RichText `json:"rich_text"`
	Color    string     `json:"color"`
}

// CodeBlock contains code content.
type CodeBlock struct {
	RichText []RichText `json:"rich_text"`
	Caption  []RichText `json:"caption"`
	Language string     `json:"language"`
}

// QuoteBlock contains quote content.
type QuoteBlock struct {
	RichText []RichText `json:"rich_text"`
	Color    string     `json:"color"`
}

// CalloutBlock contains callout content.
type CalloutBlock struct {
	RichText []RichText `json:"rich_text"`
	Icon     *Icon      `json:"icon"`
	Color    string     `json:"color"`
}

// DividerBlock is an empty struct for dividers.
type DividerBlock struct{}

// FileBlock contains file/image/video content.
type FileBlock struct {
	Type     string        `json:"type"`
	Caption  []RichText    `json:"caption"`
	External *ExternalFile `json:"external,omitempty"`
	File     *File         `json:"file,omitempty"`
	Name     string        `json:"name,omitempty"`
}

// ExternalFile represents an external file URL.
type ExternalFile struct {
	URL string `json:"url"`
}

// File represents a Notion-hosted file.
type File struct {
	URL        string    `json:"url"`
	ExpiryTime time.Time `json:"expiry_time"`
}

// BookmarkBlock contains bookmark content.
type BookmarkBlock struct {
	URL     string     `json:"url"`
	Caption []RichText `json:"caption"`
}

// EquationBlock contains equation content.
type EquationBlock struct {
	Expression string `json:"expression"`
}

// TableOfContentsBlock contains table of contents settings.
type TableOfContentsBlock struct {
	Color string `json:"color"`
}

// ChildPageBlock references a child page.
type ChildPageBlock struct {
	Title string `json:"title"`
}

// ChildDatabaseBlock references a child database.
type ChildDatabaseBlock struct {
	Title string `json:"title"`
}

// SyncedBlockBlock contains synced block reference.
type SyncedBlockBlock struct {
	SyncedFrom *SyncedFrom `json:"synced_from"`
}

// SyncedFrom references the original synced block.
type SyncedFrom struct {
	Type    string `json:"type"`
	BlockID string `json:"block_id"`
}

// TableBlock contains table settings.
type TableBlock struct {
	TableWidth      int  `json:"table_width"`
	HasColumnHeader bool `json:"has_column_header"`
	HasRowHeader    bool `json:"has_row_header"`
}

// TableRowBlock contains table row cells.
type TableRowBlock struct {
	Cells [][]RichText `json:"cells"`
}

// ColumnListBlock is a container for columns.
type ColumnListBlock struct{}

// ColumnBlock is a container for column content.
type ColumnBlock struct{}

// LinkToPageBlock references another page.
type LinkToPageBlock struct {
	Type       string `json:"type"`
	PageID     string `json:"page_id,omitempty"`
	DatabaseID string `json:"database_id,omitempty"`
}

// EmbedBlock contains embed URL.
type EmbedBlock struct {
	URL string `json:"url"`
}

// Icon represents an emoji or external icon.
type Icon struct {
	Type     string        `json:"type"`
	Emoji    string        `json:"emoji,omitempty"`
	External *ExternalFile `json:"external,omitempty"`
	File     *File         `json:"file,omitempty"`
}

// RichText represents formatted text.
type RichText struct {
	Type        string       `json:"type"`
	PlainText   string       `json:"plain_text"`
	Href        *string      `json:"href"`
	Annotations *Annotations `json:"annotations"`
	Text        *TextContent `json:"text,omitempty"`
	Mention     *Mention     `json:"mention,omitempty"`
	Equation    *Equation    `json:"equation,omitempty"`
}

// TextContent contains text content.
type TextContent struct {
	Content string `json:"content"`
	Link    *Link  `json:"link"`
}

// Link represents a URL link.
type Link struct {
	URL string `json:"url"`
}

// Mention represents a mention in rich text.
type Mention struct {
	Type string `json:"type"`
	User *User  `json:"user,omitempty"`
	Page *struct {
		ID string `json:"id"`
	} `json:"page,omitempty"`
	Database *struct {
		ID string `json:"id"`
	} `json:"database,omitempty"`
	Date *struct {
		Start string  `json:"start"`
		End   *string `json:"end"`
	} `json:"date,omitempty"`
	LinkPreview *struct {
		URL string `json:"url"`
	} `json:"link_preview,omitempty"`
}

// Equation represents an inline equation.
type Equation struct {
	Expression string `json:"expression"`
}

// Annotations contains text formatting.
type Annotations struct {
	Bold          bool   `json:"bold"`
	Italic        bool   `json:"italic"`
	Strikethrough bool   `json:"strikethrough"`
	Underline     bool   `json:"underline"`
	Code          bool   `json:"code"`
	Color         string `json:"color"`
}

// ParseRichText converts rich text array to plain string.
func ParseRichText(richText []RichText) string {
	var builder strings.Builder
	for i := range richText {
		builder.WriteString(richText[i].PlainText)
	}
	return builder.String()
}

// ParseRichTextToMarkdown converts rich text array to markdown string.
func ParseRichTextToMarkdown(richText []RichText) string {
	var builder strings.Builder
	for i := range richText {
		item := &richText[i]
		text := item.PlainText

		// Handle user mentions with formatted user info
		if item.Type == "mention" && item.Mention != nil && item.Mention.User != nil {
			text = "@" + item.Mention.User.Format()
		}

		if item.Annotations != nil {
			if item.Annotations.Code {
				text = "`" + text + "`"
			}
			if item.Annotations.Bold {
				text = "**" + text + "**"
			}
			if item.Annotations.Italic {
				text = "_" + text + "_"
			}
			if item.Annotations.Strikethrough {
				text = "~~" + text + "~~"
			}
		}
		if item.Href != nil && *item.Href != "" {
			text = "[" + text + "](" + *item.Href + ")"
		}
		builder.WriteString(text)
	}
	return builder.String()
}

// API response types

// SearchResponse represents the response from the search endpoint.
type SearchResponse struct {
	Object     string  `json:"object"`
	Results    []Page  `json:"results"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
	Type       string  `json:"type"`
}

// BlockChildrenResponse represents the response from block children endpoint.
type BlockChildrenResponse struct {
	Object     string  `json:"object"`
	Results    []Block `json:"results"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
	Type       string  `json:"type"`
}

// APIError represents a Notion API error.
type APIError struct {
	Object  string `json:"object"`
	Status  int    `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return e.Message
}

// IsPermanent returns true if this error will never resolve by retrying.
// These are errors where the resource doesn't exist, isn't shared with the
// integration, or is the wrong type.
func (e *APIError) IsPermanent() bool {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	case http.StatusBadRequest:
		return e.Code == "validation_error"
	}
	return false
}

// IsPermanentError checks if an error (possibly wrapped) is a permanent Notion API error.
func IsPermanentError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.IsPermanent()
	}
	return false
}
