package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryAuditRejectsSourceSnapshot(t *testing.T) {
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(parent, "source-only")
	if err := os.Mkdir(snapshot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := requireOwnGitRoot(snapshot); err == nil {
		t.Fatal("source-only directory must not fall back to a parent repository")
	}
}

func TestInspect(t *testing.T) {
	for _, tc := range []struct{ name, path, content, want string }{
		{"safe example", ".env.example", "DB_PASSWORD=replace-me", ""},
		{"dotenv", ".env", "value", "sensitive_or_generated_path"},
		{"known credential", "old.md", "prefix known-local-password suffix", "known_local_secret"},
		{"bearer", "old.md", "Bearer " + strings.Repeat("a", 43) + "\n", "literal_bearer"},
		{"binary", "file.bin", "a\x00b", "binary_blob"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := inspect(tc.path, []byte(tc.content), [][]byte{[]byte("known-local-password")})
			if tc.want == "" && len(got) != 0 {
				t.Fatal(got)
			}
			if tc.want != "" && !strings.Contains(strings.Join(got, ","), tc.want) {
				t.Fatalf("missing category %s", tc.want)
			}
		})
	}
}

func TestCredentialLiteralReview(t *testing.T) {
	if !needsLiteralReview([]byte(`Password = "review-this-literal"`)) {
		t.Fatal("unknown literal not flagged")
	}
	for _, s := range []string{`DB_PASSWORD = "ci-app-not-a-secret"`, `Password = "replace-with-local-value"`, `Password = config.Password`} {
		if needsLiteralReview([]byte(s)) {
			t.Fatal("public disposable value or variable should not require literal review")
		}
	}
}
