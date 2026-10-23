// release-audit scans every reachable Git blob without printing matched secrets.
// It is an aid to review, not a proof that arbitrary encodings contain no secrets.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type finding struct {
	Object   string `json:"object"`
	Path     string `json:"path"`
	Category string `json:"category"`
}
type report struct {
	Head          string    `json:"head"`
	Commits       int       `json:"commits"`
	Blobs         int       `json:"unique_blobs"`
	Bytes         int64     `json:"bytes_scanned"`
	MetadataBytes int64     `json:"commit_tag_bytes_scanned"`
	Largest       int64     `json:"largest_blob_bytes"`
	KnownSecrets  int       `json:"local_secret_values_compared"`
	Findings      []finding `json:"findings"`
	LiteralReview []finding `json:"credential_literals_for_manual_review"`
	WorktreeFiles int       `json:"worktree_files"`
	Limitations   string    `json:"limitations"`
}

var rules = map[string]*regexp.Regexp{
	"private_key":     regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`),
	"provider_token":  regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,}|AKIA[A-Z0-9]{16}|sk-(?:proj-)?[A-Za-z0-9_-]{30,})`),
	"literal_bearer":  regexp.MustCompile(`(?i)Bearer[ \t]+[A-Za-z0-9_-]{43}(?:["'\s]|$)`),
	"literal_session": regexp.MustCompile(`(?i)["']?(?:access_token|session_token)["']?\s*[:=]\s*["'][A-Za-z0-9_-]{43}["']`),
	"credential_url":  regexp.MustCompile(`(?:https?|mysql)://[^\s/:]+:[^\s/@]+@`),
}

var literalCredential = regexp.MustCompile(`(?i)(?:password|passwd|token|secret)["']?\s*[:=]\s*["']([^"'\r\n]{8,})["']`)

func needsLiteralReview(data []byte) bool {
	for _, match := range literalCredential.FindAllSubmatch(data, -1) {
		value := string(match[1])
		if !strings.Contains(value, "not-a-secret") && !strings.HasPrefix(value, "replace-") {
			return true
		}
	}
	return false
}

func generatedPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(lower)
	return (strings.HasPrefix(base, ".env") && base != ".env.example") || strings.HasPrefix(lower, ".tmp/") || strings.HasSuffix(lower, ".log") || strings.HasSuffix(lower, ".exe") || strings.HasSuffix(lower, ".sql.gz")
}

func git(args ...string) ([]byte, error) { return exec.Command("git", args...).Output() }

func localSecrets() [][]byte {
	var values [][]byte
	// Read only ignored local credential files, never copy them into the report.
	if b, err := os.ReadFile(".env"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok && (strings.Contains(strings.ToUpper(key), "PASSWORD") || strings.Contains(strings.ToUpper(key), "TOKEN")) {
				value = strings.Trim(strings.TrimSpace(value), `"'`)
				if len(value) >= 12 && !strings.Contains(value, "not-a-secret") {
					values = append(values, []byte(value))
				}
			}
		}
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, value := range x {
				if s, ok := value.(string); ok && (k == "token" || k == "access_token") && len(s) >= 32 {
					values = append(values, []byte(s))
				}
				walk(value)
			}
		case []any:
			for _, value := range x {
				walk(value)
			}
		}
	}
	if b, err := os.ReadFile(".tmp/performance/manifest.json"); err == nil {
		var value any
		if json.Unmarshal(b, &value) == nil {
			walk(value)
		}
	}
	return values
}

func inspect(path string, data []byte, secrets [][]byte) []string {
	var categories []string
	if generatedPath(path) {
		categories = append(categories, "sensitive_or_generated_path")
	}
	if len(data) > 1024*1024 {
		categories = append(categories, "large_blob_over_1mib")
	}
	if bytes.IndexByte(data, 0) >= 0 {
		categories = append(categories, "binary_blob")
	}
	for _, secret := range secrets {
		if bytes.Contains(data, secret) {
			categories = append(categories, "known_local_secret")
			break
		}
	}
	for name, rule := range rules {
		if rule.Match(data) {
			categories = append(categories, name)
		}
	}
	return categories
}

// A source-only snapshot must not accidentally inspect a repository above it.
// A future repository may use either a .git directory or a worktree .git file.
func requireOwnGitRoot(root string) error {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return fmt.Errorf("run the history audit from a repository with its own .git")
	}
	top, err := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return err
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(got), filepath.Clean(want)) {
		return fmt.Errorf("refusing to inspect a parent repository")
	}
	return nil
}

func scan() (report, error) {
	r := report{Findings: []finding{}, Limitations: "Reachable refs only; regex and current local-value comparison cannot exclude unknown, encoded or deleted-unreachable secrets. Review test fixtures, credential assignments and authors separately."}
	root, err := os.Getwd()
	if err != nil {
		return r, err
	}
	if err := requireOwnGitRoot(root); err != nil {
		return r, err
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return r, err
	}
	r.Head = strings.TrimSpace(string(head))
	n, err := git("rev-list", "--count", "--all")
	if err != nil {
		return r, err
	}
	r.Commits, _ = strconv.Atoi(strings.TrimSpace(string(n)))
	// rev-list --objects prints only one name for a reused blob. Inspect all
	// historical tree paths as well so an alias cannot hide a generated file.
	commits, err := git("rev-list", "--all")
	if err != nil {
		return r, err
	}
	for _, commit := range strings.Fields(string(commits)) {
		paths, err := git("ls-tree", "-r", "--name-only", "-z", commit)
		if err != nil {
			return r, err
		}
		for _, path := range strings.Split(string(paths), "\x00") {
			if generatedPath(path) {
				r.Findings = append(r.Findings, finding{commit, path, "historical_sensitive_path"})
			}
		}
	}
	objects, err := git("rev-list", "--objects", "--all")
	if err != nil {
		return r, err
	}
	secrets := localSecrets()
	r.KnownSecrets = len(secrets)
	cmd := exec.Command("git", "cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		return r, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return r, err
	}
	if err = cmd.Start(); err != nil {
		return r, err
	}
	defer func() { in.Close(); cmd.Wait() }()
	reader := bufio.NewReader(out)
	for _, line := range strings.Split(strings.TrimSpace(string(objects)), "\n") {
		id, path, _ := strings.Cut(line, " ")
		if _, err = fmt.Fprintln(in, id); err != nil {
			return r, err
		}
		header, err := reader.ReadString('\n')
		if err != nil {
			return r, err
		}
		parts := strings.Fields(header)
		if len(parts) != 3 {
			return r, fmt.Errorf("invalid git object header")
		}
		size, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || size < 0 || size > 64<<20 {
			return r, fmt.Errorf("object exceeds safe audit budget")
		}
		data := make([]byte, size)
		if _, err = io.ReadFull(reader, data); err != nil {
			return r, err
		}
		if _, err = reader.ReadByte(); err != nil {
			return r, err
		}
		if parts[1] == "commit" || parts[1] == "tag" {
			r.MetadataBytes += size
			for _, category := range inspect("<"+parts[1]+">", data, secrets) {
				r.Findings = append(r.Findings, finding{id, "<" + parts[1] + ">", category})
			}
		}
		if parts[1] != "blob" {
			continue
		}
		r.Blobs++
		r.Bytes += size
		if size > r.Largest {
			r.Largest = size
		}
		for _, category := range inspect(path, data, secrets) {
			r.Findings = append(r.Findings, finding{id, path, category})
		}
		if needsLiteralReview(data) {
			r.LiteralReview = append(r.LiteralReview, finding{id, path, "literal_value_review_no_value_printed"})
		}
	}
	paths, err := git("ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return r, err
	}
	for _, path := range strings.Split(string(paths), "\x00") {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return r, err
		}
		r.WorktreeFiles++
		for _, category := range inspect(path, data, secrets) {
			r.Findings = append(r.Findings, finding{"worktree", path, category})
		}
		if needsLiteralReview(data) {
			r.LiteralReview = append(r.LiteralReview, finding{"worktree", path, "literal_value_review_no_value_printed"})
		}
	}
	sort.Slice(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		return a.Path+a.Object+a.Category < b.Path+b.Object+b.Category
	})
	sort.Slice(r.LiteralReview, func(i, j int) bool {
		a, b := r.LiteralReview[i], r.LiteralReview[j]
		return a.Path+a.Object < b.Path+b.Object
	})
	return r, nil
}

func main() {
	r, err := scan()
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit failed: Git scan could not complete")
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(r)
	if len(r.Findings) > 0 {
		os.Exit(2)
	}
}
