package notion

import "testing"

func TestNormalizeID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "dashed uuid", in: "388aa28b-3ffb-80b6-9e5b-c6a0eeaebf64", want: "388aa28b3ffb80b69e5bc6a0eeaebf64"},
		{name: "already normalized", in: "388aa28b3ffb80b69e5bc6a0eeaebf64", want: "388aa28b3ffb80b69e5bc6a0eeaebf64"},
		{name: "empty", in: "", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeID(tc.in); got != tc.want {
				t.Errorf("NormalizeID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDenormalizeID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "normalized uuid", in: "388aa28b3ffb80b69e5bc6a0eeaebf64", want: "388aa28b-3ffb-80b6-9e5b-c6a0eeaebf64"},
		{name: "already dashed is left alone", in: "388aa28b-3ffb-80b6-9e5b-c6a0eeaebf64", want: "388aa28b-3ffb-80b6-9e5b-c6a0eeaebf64"},
		{name: "wrong length returned unchanged", in: "abc123", want: "abc123"},
		{name: "empty", in: "", want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DenormalizeID(tc.in); got != tc.want {
				t.Errorf("DenormalizeID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeDenormalizeRoundTrip ensures the two helpers are inverses for a
// normalized ID, which is the property the registry-lookup fallback relies on.
func TestNormalizeDenormalizeRoundTrip(t *testing.T) {
	t.Parallel()

	normalized := "388aa28b3ffb80b69e5bc6a0eeaebf64"
	if got := NormalizeID(DenormalizeID(normalized)); got != normalized {
		t.Errorf("NormalizeID(DenormalizeID(%q)) = %q, want %q", normalized, got, normalized)
	}
}
