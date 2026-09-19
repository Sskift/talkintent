package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Helper argument extractors
func getStringArg(args map[string]any, key string, def string) string {
	if args == nil {
		return def
	}
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return def
}

func getBoolArg(args map[string]any, key string, def bool) bool {
	if args == nil {
		return def
	}
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
		if s, ok := v.(string); ok {
			return strings.EqualFold(s, "true") || s == "1"
		}
	}
	return def
}

func getIntArg(args map[string]any, key string, def int) int {
	if args == nil {
		return def
	}
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
				return parsed
			}
		}
	}
	return def
}

func runGitCommand(ctx context.Context, dir, workspaceRoot string, args ...string) (string, string, error) {
	gitArgs := append([]string{"-c", "core.pager=cat"}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("")

	// Prevent Git from traversing parent directories above workspaceRoot (e.g. user home directory),
	// disable interactive credential/passphrase prompts, and enforce non-interactive pager.
	ceiling := filepath.Dir(filepath.Clean(workspaceRoot))
	cmd.Env = append(cmd.Environ(),
		"GIT_CEILING_DIRECTORIES="+ceiling,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
	)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	return outBuf.String(), errBuf.String(), err
}

// 1. GitStatusTool
type GitStatusTool struct{}

func (t *GitStatusTool) Name() string { return "git_status" }
func (t *GitStatusTool) Description() string {
	return "Show working tree status including current branch, modified files, and untracked files."
}
func (t *GitStatusTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Optional subdirectory relative to the workspace root",
			},
		},
	}
}
func (t *GitStatusTool) Execute(ctx context.Context, workspaceRoot string, args map[string]any) (string, error) {
	relPath := getStringArg(args, "path", "")
	targetDir := workspaceRoot
	if relPath != "" {
		canonical, err := ValidateSandboxPath(workspaceRoot, relPath)
		if err != nil {
			return "", err
		}
		targetDir = canonical
	}

	stdout, stderr, err := runGitCommand(ctx, targetDir, workspaceRoot, "status", "--short", "--branch")
	if err != nil {
		return fmt.Sprintf("(not a git repository or git error: %s)", strings.TrimSpace(stderr)), nil
	}

	out := strings.TrimSpace(stdout)
	if out == "" {
		return "(clean working tree, nothing to commit)", nil
	}
	return TruncateOutput(out, MaxToolOutputBytes), nil
}

// 2. GitDiffTool
type GitDiffTool struct{}

func (t *GitDiffTool) Name() string { return "git_diff" }
func (t *GitDiffTool) Description() string {
	return "Show git diff of working tree changes, staged changes, or differences against a commit."
}
func (t *GitDiffTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"cached": map[string]any{
				"type":        "boolean",
				"description": "If true, show staged changes (--cached)",
			},
			"commit": map[string]any{
				"type":        "string",
				"description": "Optional commit or branch ref to diff against, e.g. HEAD~1 or main",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional relative file or directory path within the workspace",
			},
		},
	}
}
func (t *GitDiffTool) Execute(ctx context.Context, workspaceRoot string, args map[string]any) (string, error) {
	cached := getBoolArg(args, "cached", false)
	commit := getStringArg(args, "commit", "")
	relPath := getStringArg(args, "path", "")

	gitArgs := []string{"diff"}
	if cached {
		gitArgs = append(gitArgs, "--cached")
	}
	if commit != "" {
		validCommit := regexp.MustCompile(`^[a-zA-Z0-9_\-\.\^~]+$`)
		if !validCommit.MatchString(commit) {
			return "", errors.New("invalid commit ref format")
		}
		gitArgs = append(gitArgs, commit)
	}

	if relPath != "" {
		_, err := ValidateSandboxPath(workspaceRoot, relPath)
		if err != nil {
			return "", err
		}
		gitArgs = append(gitArgs, "--", filepath.ToSlash(relPath))
	}

	stdout, stderr, err := runGitCommand(ctx, workspaceRoot, workspaceRoot, gitArgs...)
	if err != nil {
		return fmt.Sprintf("(git diff error: %s)", strings.TrimSpace(stderr)), nil
	}

	out := strings.TrimSpace(stdout)
	if out == "" {
		return "(no changes in diff)", nil
	}

	// Filter out diff chunks of denylisted files
	filtered := filterDiffOutput(out)
	return TruncateOutput(filtered, MaxToolOutputBytes), nil
}

type diffFileChunk struct {
	firstLine   string
	otherLines  []string
	isSensitive bool
}

func cleanGitDiffPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/dev/null" {
		return ""
	}
	// Handle git C-quoted strings: "a/path/to/file"
	if strings.HasPrefix(raw, "\"") && strings.HasSuffix(raw, "\"") && len(raw) >= 2 {
		if unquoted, err := strconv.Unquote(raw); err == nil {
			raw = unquoted
		} else {
			raw = strings.Trim(raw, "\"")
		}
	}
	// Strip standard git diff prefixes (a/, b/, i/, w/, c/)
	for _, prefix := range []string{"a/", "b/", "i/", "w/", "c/"} {
		if strings.HasPrefix(raw, prefix) {
			raw = strings.TrimPrefix(raw, prefix)
			break
		}
	}
	return filepath.ToSlash(filepath.Clean(raw))
}

func isDiffPathSensitive(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/dev/null" {
		return false
	}
	if IsFileDenylisted(raw) {
		return true
	}
	cleaned := cleanGitDiffPath(raw)
	if cleaned != "" && IsFileDenylisted(cleaned) {
		return true
	}
	return false
}

func tokenizeGitLine(s string) []string {
	var tokens []string
	s = strings.TrimSpace(s)
	inQuotes := false
	var cur strings.Builder

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && inQuotes && i+1 < len(s) {
			cur.WriteByte(ch)
			i++
			cur.WriteByte(s[i])
			continue
		}
		if ch == '"' {
			inQuotes = !inQuotes
			cur.WriteByte(ch)
			continue
		}
		if (ch == ' ' || ch == '\t') && !inQuotes {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
			continue
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

func extractDiffGitPaths(line string) []string {
	line = strings.TrimPrefix(line, "diff --git ")
	tokens := tokenizeGitLine(line)
	if len(tokens) >= 2 {
		return []string{tokens[0], tokens[1]}
	}
	if len(tokens) == 1 {
		return []string{tokens[0]}
	}
	return nil
}

func checkDiffLineSensitive(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}

	if strings.HasPrefix(trimmed, "diff --git ") {
		paths := extractDiffGitPaths(trimmed)
		for _, p := range paths {
			if isDiffPathSensitive(p) {
				return true
			}
		}
		return false
	}

	if strings.HasPrefix(trimmed, "diff --cc ") {
		p := strings.TrimPrefix(trimmed, "diff --cc ")
		return isDiffPathSensitive(p)
	}

	if strings.HasPrefix(trimmed, "diff --combined ") {
		p := strings.TrimPrefix(trimmed, "diff --combined ")
		return isDiffPathSensitive(p)
	}

	if strings.HasPrefix(trimmed, "Submodule ") {
		rest := strings.TrimPrefix(trimmed, "Submodule ")
		fields := strings.Fields(rest)
		if len(fields) > 0 && isDiffPathSensitive(fields[0]) {
			return true
		}
		return false
	}

	if strings.HasPrefix(trimmed, "rename from ") {
		p := strings.TrimPrefix(trimmed, "rename from ")
		return isDiffPathSensitive(p)
	}
	if strings.HasPrefix(trimmed, "rename to ") {
		p := strings.TrimPrefix(trimmed, "rename to ")
		return isDiffPathSensitive(p)
	}
	if strings.HasPrefix(trimmed, "copy from ") {
		p := strings.TrimPrefix(trimmed, "copy from ")
		return isDiffPathSensitive(p)
	}
	if strings.HasPrefix(trimmed, "copy to ") {
		p := strings.TrimPrefix(trimmed, "copy to ")
		return isDiffPathSensitive(p)
	}

	if strings.HasPrefix(trimmed, "--- ") {
		p := strings.TrimPrefix(trimmed, "--- ")
		if idx := strings.IndexAny(p, "\t\r"); idx != -1 {
			p = p[:idx]
		}
		return isDiffPathSensitive(p)
	}

	if strings.HasPrefix(trimmed, "+++ ") {
		p := strings.TrimPrefix(trimmed, "+++ ")
		if idx := strings.IndexAny(p, "\t\r"); idx != -1 {
			p = p[:idx]
		}
		return isDiffPathSensitive(p)
	}

	if strings.HasPrefix(trimmed, "Binary files ") && strings.HasSuffix(trimmed, " differ") {
		content := strings.TrimSuffix(strings.TrimPrefix(trimmed, "Binary files "), " differ")
		tokens := tokenizeGitLine(content)
		if len(tokens) >= 3 && tokens[1] == "and" {
			if isDiffPathSensitive(tokens[0]) || isDiffPathSensitive(tokens[2]) {
				return true
			}
		}
		return false
	}

	return false
}

func filterDiffOutput(diff string) string {
	lines := strings.Split(diff, "\n")
	if len(lines) == 0 {
		return diff
	}

	var chunks []*diffFileChunk
	var current *diffFileChunk

	isChunkStart := func(line string) bool {
		return strings.HasPrefix(line, "diff --git ") ||
			strings.HasPrefix(line, "diff --cc ") ||
			strings.HasPrefix(line, "diff --combined ") ||
			strings.HasPrefix(line, "Submodule ")
	}

	for _, line := range lines {
		if isChunkStart(line) {
			current = &diffFileChunk{firstLine: line}
			chunks = append(chunks, current)
			if checkDiffLineSensitive(line) {
				current.isSensitive = true
			}
			continue
		}

		if current == nil {
			current = &diffFileChunk{firstLine: line}
			chunks = append(chunks, current)
			if checkDiffLineSensitive(line) {
				current.isSensitive = true
			}
			continue
		}

		current.otherLines = append(current.otherLines, line)
		if !current.isSensitive && checkDiffLineSensitive(line) {
			current.isSensitive = true
		}
	}

	var result []string
	for _, chunk := range chunks {
		result = append(result, chunk.firstLine)
		if chunk.isSensitive {
			result = append(result, "[diff contents redacted: sensitive file in privacy denylist]")
		} else {
			result = append(result, chunk.otherLines...)
		}
	}

	return strings.Join(result, "\n")
}

// 3. GitLogTool
type GitLogTool struct{}

func (t *GitLogTool) Name() string { return "git_log" }
func (t *GitLogTool) Description() string {
	return "Show recent git commit history including hashes, authors, relative timestamps, and commit subjects."
}
func (t *GitLogTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"max_count": map[string]any{
				"type":        "integer",
				"description": "Maximum number of commits to return (default: 10, max: 50)",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional relative path within the workspace to filter commit history",
			},
		},
	}
}
func (t *GitLogTool) Execute(ctx context.Context, workspaceRoot string, args map[string]any) (string, error) {
	maxCount := getIntArg(args, "max_count", 10)
	if maxCount <= 0 {
		maxCount = 10
	}
	if maxCount > 50 {
		maxCount = 50
	}
	relPath := getStringArg(args, "path", "")

	gitArgs := []string{"log", fmt.Sprintf("-n%d", maxCount), "--pretty=format:%h - %an (%cr): %s"}
	if relPath != "" {
		_, err := ValidateSandboxPath(workspaceRoot, relPath)
		if err != nil {
			return "", err
		}
		gitArgs = append(gitArgs, "--", filepath.ToSlash(relPath))
	}

	stdout, stderr, err := runGitCommand(ctx, workspaceRoot, workspaceRoot, gitArgs...)
	if err != nil {
		return fmt.Sprintf("(git log error or not a git repository: %s)", strings.TrimSpace(stderr)), nil
	}

	out := strings.TrimSpace(stdout)
	if out == "" {
		return "(no commit history)", nil
	}
	return TruncateOutput(out, MaxToolOutputBytes), nil
}

// 4. ListDirTool
type ListDirTool struct{}

func (t *ListDirTool) Name() string { return "list_dir" }
func (t *ListDirTool) Description() string {
	return "List files and directories in a workspace directory."
}
func (t *ListDirTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative directory path within workspace (default: '.')",
			},
			"max_depth": map[string]any{
				"type":        "integer",
				"description": "Maximum recursion depth (default: 1, max: 3)",
			},
		},
	}
}
func (t *ListDirTool) Execute(ctx context.Context, workspaceRoot string, args map[string]any) (string, error) {
	relPath := getStringArg(args, "path", ".")
	maxDepth := getIntArg(args, "max_depth", 1)
	if maxDepth < 1 {
		maxDepth = 1
	}
	if maxDepth > 3 {
		maxDepth = 3
	}

	targetPath, err := ValidateSandboxPath(workspaceRoot, relPath)
	if err != nil {
		return "", err
	}

	fi, err := os.Stat(targetPath)
	if err != nil {
		return "", fmt.Errorf("path stat failed: %w", err)
	}

	if !fi.IsDir() {
		return fmt.Sprintf("[FILE] %s (%d bytes)", filepath.Base(targetPath), fi.Size()), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Directory contents of %s:\n", relPath))

	var walkErr error
	baseDepth := strings.Count(filepath.Clean(targetPath), string(filepath.Separator))

	walkErr = filepath.Walk(targetPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if path == targetPath {
			return nil
		}

		currentDepth := strings.Count(path, string(filepath.Separator)) - baseDepth
		if currentDepth > maxDepth {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		subRel, _ := filepath.Rel(workspaceRoot, path)
		if IsFileDenylisted(subRel) || IsFileDenylisted(path) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		indent := strings.Repeat("  ", currentDepth-1)
		if info.IsDir() {
			sb.WriteString(fmt.Sprintf("%s[DIR]  %s/\n", indent, info.Name()))
		} else {
			sb.WriteString(fmt.Sprintf("%s[FILE] %s (%d bytes)\n", indent, info.Name(), info.Size()))
		}
		return nil
	})

	if walkErr != nil {
		return "", fmt.Errorf("failed listing directory: %w", walkErr)
	}

	return TruncateOutput(sb.String(), MaxToolOutputBytes), nil
}

// 5. ReadFileTool
type ReadFileTool struct{}

func (t *ReadFileTool) Name() string { return "read_file" }
func (t *ReadFileTool) Description() string {
	return "Read contents of a file within the workspace root up to 64KB."
}
func (t *ReadFileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Relative path of the file to read within workspace",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "Byte offset to start reading from (default: 0)",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of bytes to read (default: 65536, max: 65536)",
			},
		},
		"required": []string{"path"},
	}
}
func (t *ReadFileTool) Execute(ctx context.Context, workspaceRoot string, args map[string]any) (string, error) {
	relPath := getStringArg(args, "path", "")
	if relPath == "" {
		return "", errors.New("read_file requires non-empty path parameter")
	}

	canonical, err := ValidateSandboxPath(workspaceRoot, relPath)
	if err != nil {
		return "", err
	}

	fi, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("failed to open file %q: %w", relPath, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%q is a directory, use list_dir instead", relPath)
	}

	f, err := os.Open(canonical)
	if err != nil {
		return "", fmt.Errorf("failed to read file %q: %w", relPath, err)
	}
	defer f.Close()

	offset := int64(getIntArg(args, "offset", 0))
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return "", fmt.Errorf("seek failed: %w", err)
		}
	}

	limit := getIntArg(args, "limit", MaxFileReadBytes)
	if limit <= 0 || limit > MaxFileReadBytes {
		limit = MaxFileReadBytes
	}

	buf := make([]byte, limit)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("read failed: %w", err)
	}

	data := buf[:n]
	truncated := false
	if fi.Size() > offset+int64(n) {
		truncated = true
	}

	out := string(data)
	if truncated {
		out += "\n[truncated by TalkIntent]"
	}
	return out, nil
}

// 6. GrepSearchTool
type GrepSearchTool struct{}

func (t *GrepSearchTool) Name() string { return "grep_search" }
func (t *GrepSearchTool) Description() string {
	return "Search file contents for a regular expression or keyword within the workspace."
}
func (t *GrepSearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regex or string pattern to search for",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional relative subdirectory or file to search in (default: '.')",
			},
			"max_matches": map[string]any{
				"type":        "integer",
				"description": "Maximum number of matching lines to return (default: 50, max: 200)",
			},
		},
		"required": []string{"pattern"},
	}
}
func (t *GrepSearchTool) Execute(ctx context.Context, workspaceRoot string, args map[string]any) (string, error) {
	pattern := getStringArg(args, "pattern", "")
	if pattern == "" {
		return "", errors.New("grep_search requires non-empty pattern parameter")
	}

	relPath := getStringArg(args, "path", ".")
	maxMatches := getIntArg(args, "max_matches", 50)
	if maxMatches <= 0 {
		maxMatches = 50
	}
	if maxMatches > 200 {
		maxMatches = 200
	}

	targetDir, err := ValidateSandboxPath(workspaceRoot, relPath)
	if err != nil {
		return "", err
	}

	re, err := regexp.Compile(pattern)
	useLiteral := false
	if err != nil {
		useLiteral = true
	}

	var matches []string
	matchCount := 0

	err = filepath.Walk(targetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		if matchCount >= maxMatches {
			return filepath.SkipAll
		}

		rel, _ := filepath.Rel(workspaceRoot, path)
		if IsFileDenylisted(rel) || IsFileDenylisted(path) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		if info.Size() > 2*1024*1024 { // Skip files > 2MB
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		// Read header to check for binary
		header := make([]byte, 512)
		hn, _ := f.Read(header)
		if bytes.Contains(header[:hn], []byte{0}) {
			return nil
		}
		_, _ = f.Seek(0, io.SeekStart)

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 256*1024)
		lineNum := 0

		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			matched := false
			if useLiteral {
				matched = strings.Contains(line, pattern)
			} else {
				matched = re.MatchString(line)
			}

			if matched {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), lineNum, strings.TrimSpace(line)))
				matchCount++
				if matchCount >= maxMatches {
					break
				}
			}
		}
		return nil
	})

	if len(matches) == 0 {
		return fmt.Sprintf("No matches found for %q in %s", pattern, relPath), nil
	}

	out := strings.Join(matches, "\n")
	return TruncateOutput(out, MaxToolOutputBytes), nil
}

// 7. RecentFilesTool
type RecentFilesTool struct{}

func (t *RecentFilesTool) Name() string { return "recent_files" }
func (t *RecentFilesTool) Description() string {
	return "List recently modified files in the workspace sorted by modification time."
}
func (t *RecentFilesTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum number of files to return (default: 15, max: 50)",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional relative subdirectory within workspace (default: '.')",
			},
		},
	}
}

type fileModItem struct {
	RelPath string
	ModTime time.Time
	Size    int64
}

func (t *RecentFilesTool) Execute(ctx context.Context, workspaceRoot string, args map[string]any) (string, error) {
	limit := getIntArg(args, "limit", 15)
	if limit <= 0 {
		limit = 15
	}
	if limit > 50 {
		limit = 50
	}
	relPath := getStringArg(args, "path", ".")

	targetDir, err := ValidateSandboxPath(workspaceRoot, relPath)
	if err != nil {
		return "", err
	}

	var items []fileModItem
	_ = filepath.Walk(targetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		rel, _ := filepath.Rel(workspaceRoot, path)
		if IsFileDenylisted(rel) || IsFileDenylisted(path) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		items = append(items, fileModItem{
			RelPath: filepath.ToSlash(rel),
			ModTime: info.ModTime(),
			Size:    info.Size(),
		})
		return nil
	})

	sort.Slice(items, func(i, j int) bool {
		return items[i].ModTime.After(items[j].ModTime)
	})

	if len(items) > limit {
		items = items[:limit]
	}

	if len(items) == 0 {
		return "(no files found)", nil
	}

	var sb strings.Builder
	sb.WriteString("Recently modified files:\n")
	for _, it := range items {
		sb.WriteString(fmt.Sprintf("%s  %8d bytes  %s\n", it.ModTime.Format("2006-01-02 15:04:05"), it.Size, it.RelPath))
	}
	return sb.String(), nil
}

// 8. ListeningPortsTool
type ListeningPortsTool struct{}

func (t *ListeningPortsTool) Name() string { return "listening_ports" }
func (t *ListeningPortsTool) Description() string {
	return "List active TCP listening ports on the local machine to discover running services."
}
func (t *ListeningPortsTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}
func (t *ListeningPortsTool) Execute(ctx context.Context, workspaceRoot string, args map[string]any) (string, error) {
	if runtime.GOOS == "windows" {
		return parseWindowsListeningPorts(ctx)
	}
	return parseUnixListeningPorts(ctx)
}

var privateIPRegexp = regexp.MustCompile(`\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|172\.(1[6-9]|2[0-9]|3[0-1])\.\d{1,3}\.\d{1,3})\b`)

func sanitizeListeningOutput(text string) string {
	return privateIPRegexp.ReplaceAllString(text, "0.0.0.0")
}

func sanitizeListeningAddress(hostPort string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(hostPort))
	if err != nil {
		return sanitizeListeningOutput(hostPort)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return sanitizeListeningOutput(hostPort)
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return net.JoinHostPort(host, port)
	}
	if ip.To4() != nil {
		return net.JoinHostPort("0.0.0.0", port)
	}
	return net.JoinHostPort("::", port)
}

func parseWindowsListeningPorts(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "netstat", "-ano", "-p", "tcp")
	out, err := cmd.Output()
	if err != nil {
		// Fallback to netstat -ano
		cmd = exec.CommandContext(ctx, "netstat", "-ano")
		out, err = cmd.Output()
		if err != nil {
			return "(unable to inspect listening ports via netstat)", nil
		}
	}

	var sb strings.Builder
	sb.WriteString("Active TCP Listening Ports:\n")
	sb.WriteString(fmt.Sprintf("%-6s %-25s %-12s %s\n", "Proto", "Local Address", "State", "PID"))

	scanner := bufio.NewScanner(bytes.NewReader(out))
	count := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(line, "LISTENING") {
			fields := strings.Fields(line)
			if len(fields) >= 5 && strings.EqualFold(fields[0], "TCP") {
				proto := fields[0]
				localAddr := sanitizeListeningAddress(fields[1])
				state := fields[3]
				pid := fields[4]
				sb.WriteString(fmt.Sprintf("%-6s %-25s %-12s %s\n", proto, localAddr, state, pid))
				count++
			}
		}
	}

	if count == 0 {
		return "No TCP listening ports found.", nil
	}
	return sb.String(), nil
}

func parseUnixListeningPorts(ctx context.Context) (string, error) {
	// 1. Try /proc/net/tcp and /proc/net/tcp6 first in pure Go
	entries := readProcNetTCP("/proc/net/tcp")
	entries = append(entries, readProcNetTCP("/proc/net/tcp6")...)
	if len(entries) > 0 {
		var sb strings.Builder
		sb.WriteString("Active TCP Listening Ports:\n")
		sb.WriteString(fmt.Sprintf("%-6s %-25s %s\n", "Proto", "Local Address", "State"))
		for _, e := range entries {
			sb.WriteString(fmt.Sprintf("%-6s %-25s %s\n", "TCP", e, "LISTEN"))
		}
		return sb.String(), nil
	}

	// 2. Try ss -tlpn
	if cmd := exec.CommandContext(ctx, "ss", "-tlpn"); cmd != nil {
		if out, err := cmd.Output(); err == nil && len(out) > 0 {
			return "Active TCP Listening Ports (via ss):\n" + sanitizeListeningOutput(string(out)), nil
		}
	}

	// 3. Try lsof -iTCP -sTCP:LISTEN -P -n
	if cmd := exec.CommandContext(ctx, "lsof", "-iTCP", "-sTCP:LISTEN", "-P", "-n"); cmd != nil {
		if out, err := cmd.Output(); err == nil && len(out) > 0 {
			return "Active TCP Listening Ports (via lsof):\n" + sanitizeListeningOutput(string(out)), nil
		}
	}

	// 4. Try netstat -tlpn
	if cmd := exec.CommandContext(ctx, "netstat", "-tlpn"); cmd != nil {
		if out, err := cmd.Output(); err == nil && len(out) > 0 {
			return "Active TCP Listening Ports (via netstat):\n" + sanitizeListeningOutput(string(out)), nil
		}
	}

	// 5. Probe common local ports as fallback
	commonPorts := []int{3000, 3306, 5000, 5432, 6379, 8000, 8080, 8081, 9000, 9090}
	var open []int
	for _, p := range commonPorts {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), 15*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			open = append(open, p)
		}
	}

	if len(open) > 0 {
		var sb strings.Builder
		sb.WriteString("Active Local Listening Ports (probed):\n")
		for _, p := range open {
			sb.WriteString(fmt.Sprintf("127.0.0.1:%d (OPEN)\n", p))
		}
		return sb.String(), nil
	}

	return "No active TCP listening ports discovered.", nil
}

func readProcNetTCP(procPath string) []string {
	f, err := os.Open(procPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var result []string
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(f)
	firstLine := true

	for scanner.Scan() {
		if firstLine {
			firstLine = false
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		// state 0A is TCP_LISTEN
		if fields[3] != "0A" {
			continue
		}

		addrParts := strings.Split(fields[1], ":")
		if len(addrParts) != 2 {
			continue
		}

		ipHex := addrParts[0]
		portHex := addrParts[1]

		port, err := strconv.ParseInt(portHex, 16, 64)
		if err != nil {
			continue
		}

		var ip net.IP
		if len(ipHex) == 8 {
			ipBytes, err := hex.DecodeString(ipHex)
			if err == nil && len(ipBytes) == 4 {
				// /proc/net/tcp stores in little endian
				ip = net.IPv4(ipBytes[3], ipBytes[2], ipBytes[1], ipBytes[0])
			}
		} else if len(ipHex) == 32 {
			ipBytes, err := hex.DecodeString(ipHex)
			if err == nil && len(ipBytes) == 16 {
				// /proc/net/tcp6 stores in four 32-bit words, each little endian
				var b [16]byte
				for w := 0; w < 4; w++ {
					b[w*4+0] = ipBytes[w*4+3]
					b[w*4+1] = ipBytes[w*4+2]
					b[w*4+2] = ipBytes[w*4+1]
					b[w*4+3] = ipBytes[w*4+0]
				}
				ip = net.IP(b[:])
			}
		}

		var ipStr string
		if ip == nil {
			ipStr = "0.0.0.0"
		} else if ip.IsLoopback() || ip.IsUnspecified() {
			ipStr = ip.String()
		} else {
			// Filter/mask internal, private, or LAN IP addresses to prevent leaking host topology
			if ip.To4() != nil {
				ipStr = "0.0.0.0"
			} else {
				ipStr = "::"
			}
		}

		entry := fmt.Sprintf("%s:%d", ipStr, port)
		if _, ok := seen[entry]; !ok {
			seen[entry] = struct{}{}
			result = append(result, entry)
		}
	}
	return result
}

// 9. FindAPISpecsTool
type FindAPISpecsTool struct{}

func (t *FindAPISpecsTool) Name() string { return "find_api_specs" }
func (t *FindAPISpecsTool) Description() string {
	return "Search workspace for API specification files (OpenAPI, Swagger, protobuf, GraphQL, gRPC)."
}
func (t *FindAPISpecsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Optional relative directory to search within (default: '.')",
			},
		},
	}
}
func (t *FindAPISpecsTool) Execute(ctx context.Context, workspaceRoot string, args map[string]any) (string, error) {
	relPath := getStringArg(args, "path", ".")
	targetDir, err := ValidateSandboxPath(workspaceRoot, relPath)
	if err != nil {
		return "", err
	}

	var found []string
	_ = filepath.Walk(targetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil
		}
		rel, _ := filepath.Rel(workspaceRoot, path)
		if IsFileDenylisted(rel) || IsFileDenylisted(path) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		nameLower := strings.ToLower(info.Name())
		isSpec := false
		specType := ""

		if strings.HasSuffix(nameLower, ".proto") {
			isSpec = true
			specType = "Protobuf / gRPC"
		} else if strings.Contains(nameLower, "openapi") && (strings.HasSuffix(nameLower, ".json") || strings.HasSuffix(nameLower, ".yaml") || strings.HasSuffix(nameLower, ".yml")) {
			isSpec = true
			specType = "OpenAPI"
		} else if strings.Contains(nameLower, "swagger") && (strings.HasSuffix(nameLower, ".json") || strings.HasSuffix(nameLower, ".yaml") || strings.HasSuffix(nameLower, ".yml")) {
			isSpec = true
			specType = "Swagger"
		} else if strings.HasSuffix(nameLower, ".graphql") || strings.HasSuffix(nameLower, ".gql") {
			isSpec = true
			specType = "GraphQL"
		} else if nameLower == "api.yaml" || nameLower == "api.yml" || nameLower == "api.json" {
			isSpec = true
			specType = "API Spec"
		}

		if isSpec {
			found = append(found, fmt.Sprintf("- %s (%s, %d bytes)", filepath.ToSlash(rel), specType, info.Size()))
		}
		return nil
	})

	if len(found) == 0 {
		return "No API specification files (OpenAPI, Swagger, Protobuf, GraphQL) found in workspace.", nil
	}

	return fmt.Sprintf("Found %d API specification file(s):\n%s", len(found), strings.Join(found, "\n")), nil
}
