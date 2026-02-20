package notion

import (
	"errors"
	"fmt"
	"testing"

	"github.com/fclairamb/ntnsync/internal/apperrors"
)

var errNetworkTimeout = errors.New("network timeout")

func TestUserFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		user *User
		want string
	}{
		{
			name: "nil user",
			user: nil,
			want: "",
		},
		{
			name: "person with email",
			user: &User{
				ID:   "abc12345-6789-abcd-efgh",
				Type: "person",
				Name: "John Doe",
				Person: &Person{
					Email: "john@example.com",
				},
			},
			want: "John Doe <john@example.com> [abc12345]",
		},
		{
			name: "person without email",
			user: &User{
				ID:   "def67890-1234-abcd-efgh",
				Type: "person",
				Name: "Jane Smith",
			},
			want: "Jane Smith [def67890]",
		},
		{
			name: "person with nil person struct",
			user: &User{
				ID:     "ghi11111-2222-3333-4444",
				Type:   "person",
				Name:   "Bob Wilson",
				Person: nil,
			},
			want: "Bob Wilson [ghi11111]",
		},
		{
			name: "person with empty email",
			user: &User{
				ID:   "jkl55555-6666-7777-8888",
				Type: "person",
				Name: "Alice Brown",
				Person: &Person{
					Email: "",
				},
			},
			want: "Alice Brown [jkl55555]",
		},
		{
			name: "bot user",
			user: &User{
				ID:   "bot12345-6789-abcd-efgh",
				Type: "bot",
				Name: "Integration Bot",
			},
			want: "Integration Bot [bot12345]",
		},
		{
			name: "user with no name",
			user: &User{
				ID: "12345678-abcd-efgh-ijkl",
			},
			want: "Unknown [12345678]",
		},
		{
			name: "user with short ID",
			user: &User{
				ID:   "abc",
				Name: "Short ID User",
			},
			want: "Short ID User [abc]",
		},
		{
			name: "user with exactly 8 char ID",
			user: &User{
				ID:   "12345678",
				Name: "Exact User",
			},
			want: "Exact User [12345678]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.user.Format()
			if got != tt.want {
				t.Errorf("User.Format() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseRichTextToMarkdown_UserMention(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		richText []RichText
		want     string
	}{
		{
			name: "user mention with full info",
			richText: []RichText{
				{
					Type:      "mention",
					PlainText: "@John Doe",
					Mention: &Mention{
						Type: "user",
						User: &User{
							ID:   "abc12345-6789",
							Type: "person",
							Name: "John Doe",
							Person: &Person{
								Email: "john@example.com",
							},
						},
					},
				},
			},
			want: "@John Doe <john@example.com> [abc12345]",
		},
		{
			name: "user mention without email",
			richText: []RichText{
				{
					Type:      "mention",
					PlainText: "@Jane",
					Mention: &Mention{
						Type: "user",
						User: &User{
							ID:   "def67890-1234",
							Type: "person",
							Name: "Jane Smith",
						},
					},
				},
			},
			want: "@Jane Smith [def67890]",
		},
		{
			name: "mixed text with user mention",
			richText: []RichText{
				{
					Type:      "text",
					PlainText: "Hello ",
				},
				{
					Type:      "mention",
					PlainText: "@John",
					Mention: &Mention{
						Type: "user",
						User: &User{
							ID:   "abc12345-6789",
							Type: "person",
							Name: "John Doe",
							Person: &Person{
								Email: "john@example.com",
							},
						},
					},
				},
				{
					Type:      "text",
					PlainText: ", how are you?",
				},
			},
			want: "Hello @John Doe <john@example.com> [abc12345], how are you?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ParseRichTextToMarkdown(tt.richText)
			if got != tt.want {
				t.Errorf("ParseRichTextToMarkdown() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAPIError_IsPermanent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  APIError
		want bool
	}{
		{
			name: "404 object_not_found",
			err:  APIError{Status: 404, Code: "object_not_found", Message: "Could not find block"},
			want: true,
		},
		{
			name: "401 unauthorized",
			err:  APIError{Status: 401, Code: "unauthorized", Message: "API token is invalid"},
			want: true,
		},
		{
			name: "403 restricted_resource",
			err:  APIError{Status: 403, Code: "restricted_resource", Message: "Not allowed"},
			want: true,
		},
		{
			name: "400 validation_error",
			err:  APIError{Status: 400, Code: "validation_error", Message: "is a block, not a page"},
			want: true,
		},
		{
			name: "400 invalid_json",
			err:  APIError{Status: 400, Code: "invalid_json", Message: "bad json"},
			want: false,
		},
		{
			name: "429 rate_limited",
			err:  APIError{Status: 429, Code: "rate_limited", Message: "Rate limited"},
			want: false,
		},
		{
			name: "500 internal_server_error",
			err:  APIError{Status: 500, Code: "internal_server_error", Message: "Internal error"},
			want: false,
		},
		{
			name: "502 bad gateway",
			err:  APIError{Status: 502, Code: "", Message: "Bad Gateway"},
			want: false,
		},
		{
			name: "503 service_unavailable",
			err:  APIError{Status: 503, Code: "service_unavailable", Message: "Service unavailable"},
			want: false,
		},
		{
			name: "409 conflict_error",
			err:  APIError{Status: 409, Code: "conflict_error", Message: "Conflict"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.err.IsPermanent(); got != tt.want {
				t.Errorf("APIError{Status: %d, Code: %q}.IsPermanent() = %v, want %v",
					tt.err.Status, tt.err.Code, got, tt.want)
			}
		})
	}
}

func TestIsPermanentError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "direct permanent error",
			err:  &APIError{Status: 404, Code: "object_not_found", Message: "not found"},
			want: true,
		},
		{
			name: "wrapped permanent error",
			err:  fmt.Errorf("fetch page: %w", &APIError{Status: 404, Code: "object_not_found", Message: "not found"}),
			want: true,
		},
		{
			name: "double wrapped permanent error",
			err:  fmt.Errorf("process: %w", fmt.Errorf("fetch page: %w", &APIError{Status: 404, Code: "object_not_found", Message: "not found"})),
			want: true,
		},
		{
			name: "wrapped transient error",
			err:  fmt.Errorf("fetch page: %w", &APIError{Status: 500, Code: "internal_server_error", Message: "oops"}),
			want: false,
		},
		{
			name: "non-API error",
			err:  errNetworkTimeout,
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "ErrNoDataSources wrapped",
			err:  fmt.Errorf("fetch database: database abc: %w", apperrors.ErrNoDataSources),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := IsPermanentError(tt.err); got != tt.want {
				t.Errorf("IsPermanentError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
