package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrSandboxViolation indicates a path escapes the authorized workspace root.
var ErrSandboxViolation = errors.New("access denied: path escapes workspace root")

// ErrDenylistedFile indicates the requested file matches a sensitive denylist pattern.
var ErrDenylistedFile = errors.New("access denied: file is in hard privacy denylist")

// MaxFileReadBytes is the default byte cap for reading files (64 KB).
const MaxFileReadBytes = 64 * 1024

// MaxToolOutputBytes is the maximum byte cap for tool output like git_diff and grep_search (64 KB).
const MaxToolOutputBytes = 64 * 1024

// HardDenylistGlobs contains patterns that the probe agent must never inspect.
var HardDenylistGlobs = []string{
	"*.pem", "*.key", "*.crt", "*.pfx", "*.p12",
	"id_rsa*", "id_ed25519*", "*.pub",
	".env", ".env.*", "*secret*", "*credential*", "*token*", "*password*",
	".git/config", "*aws/credentials*", "*aws/config*", "*.ssh/*", "*.gnupg/*", "*gcloud/*",
}

// Tool represents a sandboxed read-only capability for the probe agent.
type Tool interface {
	Name() string
	Description() string
	Parameters() map[string]any
	Execute(ctx context.Context, workspaceRoot string, args map[string]any) (string, error)
}

// Registry manages the set of available probe tools.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry creates a registry populated with default read-only tools.
func NewRegistry() *Registry {
	r := &Registry{tools: make(map[string]Tool)}
	return r
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// Get finds a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools.
func (r *Registry) List() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

func normalizeVolume(path string) string {
	vol := filepath.VolumeName(path)
	if vol == "" {
		return path
	}
	return strings.ToUpper(vol) + path[len(vol):]
}

// resolvePhysicalPath resolves symbolic links for target.
// If target does not exist, resolves symlinks for its closest existing ancestor.
func resolvePhysicalPath(target string) (string, error) {
	clean := filepath.Clean(target)
	if _, err := os.Lstat(clean); err == nil {
		return filepath.EvalSymlinks(clean)
	}

	// Target does not exist; climb parents to resolve symlinks in the directory chain
	dir := filepath.Dir(clean)
	base := filepath.Base(clean)
	if dir == clean || dir == "." || dir == string(filepath.Separator) {
		return clean, nil
	}

	resolvedDir, err := resolvePhysicalPath(dir)
	if err != nil {
		return clean, nil
	}
	return filepath.Join(resolvedDir, base), nil
}

// ValidateSandboxPath verifies that relOrAbsPath stays strictly within workspaceRoot,
// resolving all symbolic links to prevent sandbox escape.
// Returns the canonical physical path if valid.
func ValidateSandboxPath(workspaceRoot, relOrAbsPath string) (string, error) {
	absRoot, err := filepath.Abs(filepath.Clean(workspaceRoot))
	if err != nil {
		return "", fmt.Errorf("invalid workspace root: %w", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		canonicalRoot = absRoot
	}
	canonicalRoot = normalizeVolume(canonicalRoot)

	target := relOrAbsPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(absRoot, target)
	}
	cleanTarget := filepath.Clean(target)

	canonicalTarget, err := resolvePhysicalPath(cleanTarget)
	if err != nil {
		canonicalTarget = cleanTarget
	}
	canonicalTarget = normalizeVolume(canonicalTarget)

	// Check volume matching on Windows
	if filepath.VolumeName(canonicalRoot) != filepath.VolumeName(canonicalTarget) {
		return "", ErrSandboxViolation
	}

	rel, err := filepath.Rel(canonicalRoot, canonicalTarget)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", ErrSandboxViolation
	}

	// Apply denylist check to both logical path and resolved physical path
	if IsFileDenylisted(cleanTarget) || IsFileDenylisted(canonicalTarget) || IsFileDenylisted(rel) {
		return "", ErrDenylistedFile
	}

	return canonicalTarget, nil
}

// IsFileDenylisted checks if the filename or path matches any sensitive hard-denylist glob
// or resides in restricted directories (.git/, .ssh/, .aws/, .gnupg/, gcloud/).
func IsFileDenylisted(relOrAbsPath string) bool {
	clean := strings.ToLower(filepath.ToSlash(filepath.Clean(relOrAbsPath)))
	base := strings.ToLower(filepath.Base(clean))

	// Explicit directory denylist rules
	if clean == ".git/config" || strings.HasSuffix(clean, "/.git/config") {
		return true
	}
	if strings.Contains(clean, "/.git/") || strings.HasPrefix(clean, ".git/") ||
		strings.Contains(clean, "/.ssh/") || strings.HasPrefix(clean, ".ssh/") ||
		strings.Contains(clean, "/.aws/") || strings.HasPrefix(clean, ".aws/") ||
		strings.Contains(clean, "/.gnupg/") || strings.HasPrefix(clean, ".gnupg/") ||
		strings.Contains(clean, "/.config/gcloud/") || strings.HasPrefix(clean, ".config/gcloud/") ||
		strings.Contains(clean, "aws/credentials") || strings.Contains(clean, "aws/config") {
		return true
	}

	for _, pattern := range HardDenylistGlobs {
		p := strings.ToLower(pattern)
		if matched, _ := filepath.Match(p, base); matched {
			return true
		}
		if matched, _ := filepath.Match(p, clean); matched {
			return true
		}
		if strings.Contains(clean, strings.TrimPrefix(strings.TrimSuffix(p, "*"), "*")) {
			if strings.Contains(p, "secret") || strings.Contains(p, "credential") ||
				strings.Contains(p, "token") || strings.Contains(p, "password") ||
				strings.Contains(p, ".env") || strings.Contains(p, "aws") ||
				strings.Contains(p, "ssh") || strings.Contains(p, "gnupg") || strings.Contains(p, "gcloud") {
				return true
			}
		}
	}
	return false
}

// TruncateOutput truncates output strings exceeding maxBytes.
func TruncateOutput(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n[truncated by TalkIntent]"
}
