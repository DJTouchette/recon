package git

import "testing"

func TestParseLogOutput(t *testing.T) {
	input := `abc123def456
John Doe
2024-01-15 10:30:00 +0000
Fix authentication bug

src/auth/login.go
src/auth/login_test.go

def456abc789
Jane Smith
2024-01-14 09:00:00 +0000
Add user profile endpoint

src/api/profile.go
src/api/routes.go
src/models/user.go
`

	commits := parseLogOutput([]byte(input))

	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}

	c := commits[0]
	if c.Hash != "abc123def456" {
		t.Errorf("hash = %q, want %q", c.Hash, "abc123def456")
	}
	if c.Author != "John Doe" {
		t.Errorf("author = %q, want %q", c.Author, "John Doe")
	}
	if c.Message != "Fix authentication bug" {
		t.Errorf("message = %q, want %q", c.Message, "Fix authentication bug")
	}
	if len(c.Files) != 2 {
		t.Errorf("files = %d, want 2", len(c.Files))
	}

	c2 := commits[1]
	if len(c2.Files) != 3 {
		t.Errorf("files = %d, want 3", len(c2.Files))
	}
}

func TestConvertSince(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"7d", "7 days ago"},
		{"2w", "2 weeks ago"},
		{"1m", "1 months ago"},
		{"", "7 days ago"},
		{"2024-01-01", "2024-01-01"},
	}

	for _, tt := range tests {
		got := convertSince(tt.input)
		if got != tt.want {
			t.Errorf("convertSince(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
