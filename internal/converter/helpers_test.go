package converter

import (
	"strings"
	"testing"
)

func TestSanitizeFilename_BasicConversion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "uppercase to lowercase",
			input: "MyPage",
			want:  "mypage",
		},
		{
			name:  "spaces to dashes",
			input: "my page",
			want:  "my-page",
		},
		{
			name:  "special chars removed",
			input: "my@page#test",
			want:  "mypagetest",
		},
		{
			name:  "mixed case and spaces",
			input: "My Cool Page",
			want:  "my-cool-page",
		},
		{
			name:  "underscores to dashes",
			input: "my_page_test",
			want:  "my-page-test",
		},
		{
			name:  "slashes to dashes",
			input: "my/page/test",
			want:  "my-page-test",
		},
		{
			name:  "backslashes to dashes",
			input: "my\\page\\test",
			want:  "my-page-test",
		},
		{
			name:  "colons to dashes",
			input: "my:page:test",
			want:  "my-page-test",
		},
		{
			name:  "pipes to dashes",
			input: "my|page|test",
			want:  "my-page-test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeFilename_DashCollapse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "multiple dashes collapsed",
			input: "my--page",
			want:  "my-page",
		},
		{
			name:  "many dashes collapsed",
			input: "my-----page",
			want:  "my-page",
		},
		{
			name:  "multiple separators collapsed",
			input: "my  page",
			want:  "my-page",
		},
		{
			name:  "leading dashes removed",
			input: "--mypage",
			want:  "mypage",
		},
		{
			name:  "trailing dashes removed",
			input: "mypage--",
			want:  "mypage",
		},
		{
			name:  "leading and trailing dashes removed",
			input: "--mypage--",
			want:  "mypage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeFilename_StartWithLetter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "numeric prefix removed",
			input: "123page",
			want:  "page",
		},
		{
			name:  "dash prefix removed",
			input: "-page",
			want:  "page",
		},
		{
			name:  "multiple non-letter chars removed",
			input: "123-abc",
			want:  "abc",
		},
		{
			name:  "starts with letter kept",
			input: "a123",
			want:  "a123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeFilename_Truncation(t *testing.T) {
	t.Parallel()

	// Create a long string that exceeds maxFilenameLength
	longName := strings.Repeat("a", maxFilenameLength+50)
	got := SanitizeFilename(longName)

	if len(got) > maxFilenameLength {
		t.Errorf("SanitizeFilename(long string) = %d chars, want <= %d", len(got), maxFilenameLength)
	}

	// Verify it doesn't end with a dash after truncation
	if strings.HasSuffix(got, "-") {
		t.Errorf("SanitizeFilename(long string) ends with dash: %q", got)
	}
}

func TestSanitizeFilename_Empty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "empty string",
			input: "",
		},
		{
			name:  "only special chars",
			input: "@#$%",
		},
		{
			name:  "only numbers",
			input: "12345",
		},
		{
			name:  "only dashes",
			input: "---",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizeFilename(tt.input)
			if got != defaultUntitledStr {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.input, got, defaultUntitledStr)
			}
		})
	}
}

func TestSanitizeFilename_UnicodeChars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "emoji removed",
			input: "my🎉page",
			want:  "mypage",
		},
		{
			name:  "chinese chars removed",
			input: "my页面page", //nolint:gosmopolitan // Testing non-ASCII character removal
			want:  "mypage",
		},
		{
			name:  "accented chars removed",
			input: "café",
			want:  "caf",
		},
		{
			name:  "mixed unicode",
			input: "test😊café🎉",
			want:  "testcaf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeID_RemovesDashes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "UUID with dashes",
			input: "123e4567-e89b-12d3-a456-426614174000",
			want:  "123e4567e89b12d3a456426614174000",
		},
		{
			name:  "no dashes",
			input: "123e4567e89b12d3a456426614174000",
			want:  "123e4567e89b12d3a456426614174000",
		},
		{
			name:  "single dash",
			input: "abc-def",
			want:  "abcdef",
		},
		{
			name:  "multiple dashes",
			input: "a-b-c-d",
			want:  "abcd",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := NormalizeID(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
