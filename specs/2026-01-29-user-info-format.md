# User Information Format

## Problem

When syncing Notion pages, user references (created_by, last_edited_by, @mentions, people properties) only show the user's ID, which is not human-readable.

**Current behavior:**
- Users are stored as just an ID: `12345678-1234-1234-1234-123456789abc`
- No way to know who created or edited a page without looking up the ID in Notion

**Expected behavior:**
- Users should display in a consistent format: `Name <email> [uid]`
- Example: `John Doe <john.doe@gmail.com> [abc12345]`

## Root Cause

In `internal/notion/types.go`, the `User` struct (lines 132-136) only captures:

```go
type User struct {
    Object string `json:"object"`
    ID     string `json:"id"`
}
```

However, the Notion API returns additional fields:
- `name` - User's display name
- `type` - Either "person" or "bot"
- `avatar_url` - URL to avatar image (optional)
- `person.email` - Email address (only for person type)

## Proposed Solution

### 1. Expand the User Struct

Update `internal/notion/types.go`:

```go
// User represents a Notion user reference.
type User struct {
    Object    string  `json:"object"`
    ID        string  `json:"id"`
    Type      string  `json:"type,omitempty"`      // "person" or "bot"
    Name      string  `json:"name,omitempty"`
    AvatarURL *string `json:"avatar_url,omitempty"`
    Person    *Person `json:"person,omitempty"`
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
```

### 2. Add Formatting Method

Add a method to format user info in a consistent, readable way:

```go
// Format returns the user in a human-readable format.
// Format: "Name <email> [short_id]"
// - Name defaults to "Unknown" if empty
// - Email is omitted if not available (person without email, or bot)
// - Short ID is first 8 characters of the UUID
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
    if len(shortID) > 8 {
        shortID = shortID[:8]
    }

    // Person with email: "Name <email> [id]"
    if u.Type == "person" && u.Person != nil && u.Person.Email != "" {
        return fmt.Sprintf("%s <%s> [%s]", name, u.Person.Email, shortID)
    }

    // No email available: "Name [id]"
    return fmt.Sprintf("%s [%s]", name, shortID)
}
```

### 3. Update Frontmatter

In `internal/converter/converter.go`, update `generateFrontmatter()` to include user info:

```yaml
---
notion_id: abc123
title: "My Page"
created_by: "John Doe <john@example.com> [abc12345]"
last_edited_by: "Jane Smith <jane@example.com> [def67890]"
last_edited: 2026-01-29T10:00:00Z
---
```

### 4. Update User Mentions in Rich Text

When parsing rich text with user mentions, display the formatted user:

```go
// In ParseRichTextToMarkdown
if item.Mention != nil && item.Mention.User != nil {
    // Renders as: @John Doe <john@example.com> [abc12345]
    text = "@" + item.Mention.User.Format()
}
```

## Display Format Summary

| User Type | Has Email | Output Format |
|-----------|-----------|---------------|
| Person | Yes | `John Doe <john@example.com> [abc12345]` |
| Person | No | `John Doe [abc12345]` |
| Bot | - | `My Bot [abc12345]` |
| Unknown | - | `Unknown [abc12345]` |

The format is consistent: always includes the name and UID, with email when available. The UID in brackets allows for easy parsing and cross-referencing with the Notion API.

## Files to Modify

1. **`internal/notion/types.go`**
   - Expand `User` struct with new fields
   - Add `Person` and `Bot` structs
   - Add `Format()` method

2. **`internal/notion/types_test.go`** (create if needed)
   - Add tests for `User.Format()` method

3. **`internal/converter/converter.go`**
   - Update `generateFrontmatter()` to include `created_by` and `last_edited_by`

4. **`internal/notion/types.go`** (ParseRichTextToMarkdown)
   - Update user mention handling to use `Format()`

## Test Cases

```go
func TestUserFormat(t *testing.T) {
    tests := []struct {
        name string
        user User
        want string
    }{
        {
            name: "person with email",
            user: User{
                ID:   "abc12345-6789-abcd-efgh",
                Type: "person",
                Name: "John Doe",
                Person: &Person{Email: "john@example.com"},
            },
            want: "John Doe <john@example.com> [abc12345]",
        },
        {
            name: "person without email",
            user: User{
                ID:   "def67890-1234-abcd-efgh",
                Type: "person",
                Name: "Jane Smith",
            },
            want: "Jane Smith [def67890]",
        },
        {
            name: "bot",
            user: User{
                ID:   "bot12345-6789-abcd-efgh",
                Type: "bot",
                Name: "Integration Bot",
            },
            want: "Integration Bot [bot12345]",
        },
        {
            name: "minimal user (no name)",
            user: User{
                ID: "12345678-abcd-efgh-ijkl",
            },
            want: "Unknown [12345678]",
        },
    }
    // ...
}
```

## Backward Compatibility

- Existing frontmatter fields are preserved
- New `created_by` and `last_edited_by` fields are additions
- No breaking changes to existing sync behavior
- Pages will update to include user info on next sync
