package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/Sskift/talkintent/internal/config"
)

// WorkspaceOptions holds arguments for workspace subcommands.
type WorkspaceOptions struct {
	ConfigPath string
	Subcommand string // "list", "add", "remove"
	Path       string
	Name       string
	Target     string
	JSONOutput bool
}

// ExecuteWorkspace handles workspace add, list, and remove commands.
func ExecuteWorkspace(opts WorkspaceOptions, stdout, stderr io.Writer) int {
	cfg, err := config.LoadClientConfig(opts.ConfigPath)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to load client config: %v. Run 'talkintent pair' first.\n", err)
		return 1
	}

	switch opts.Subcommand {
	case "list", "ls", "":
		return executeWorkspaceList(cfg, opts, stdout)
	case "add":
		return executeWorkspaceAdd(cfg, opts, stdout, stderr)
	case "remove", "rm":
		return executeWorkspaceRemove(cfg, opts, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown workspace action %q. Use 'list', 'add', or 'remove'.\n", opts.Subcommand)
		return 1
	}
}

func executeWorkspaceList(cfg *config.ClientConfig, opts WorkspaceOptions, stdout io.Writer) int {
	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"total":      len(cfg.Workspaces),
			"workspaces": cfg.Workspaces,
		})
		return 0
	}
	PrintWorkspacesTable(stdout, cfg.Workspaces)
	return 0
}

func executeWorkspaceAdd(cfg *config.ClientConfig, opts WorkspaceOptions, stdout, stderr io.Writer) int {
	dirPath := strings.TrimSpace(opts.Path)
	if dirPath == "" {
		fmt.Fprintf(stderr, "Usage: talkintent workspace add <directory_path> [name]\n")
		return 1
	}

	norm, err := config.NormalizeWorkspacePath(dirPath)
	if err != nil {
		fmt.Fprintf(stderr, "Invalid directory path %q: %v\n", dirPath, err)
		return 1
	}

	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = filepath.Base(norm)
	}

	ws := config.WorkspaceConfig{
		Name:     name,
		RootPath: norm,
	}

	if err := cfg.AddWorkspace(ws); err != nil {
		fmt.Fprintf(stderr, "Failed to add workspace: %v\n", err)
		return 1
	}

	if err := config.SaveClientConfig(opts.ConfigPath, cfg); err != nil {
		fmt.Fprintf(stderr, "Failed to persist workspace configuration: %v\n", err)
		return 1
	}

	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"status": "added",
			"name":   name,
			"path":   norm,
		})
		return 0
	}

	fmt.Fprintf(stdout, "Successfully added workspace: %s (%s)\n", name, norm)
	return 0
}

func executeWorkspaceRemove(cfg *config.ClientConfig, opts WorkspaceOptions, stdout, stderr io.Writer) int {
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		target = strings.TrimSpace(opts.Path)
	}
	if target == "" {
		fmt.Fprintf(stderr, "Usage: talkintent workspace remove <name_or_id_or_path>\n")
		return 1
	}

	if !cfg.RemoveWorkspace(target) {
		fmt.Fprintf(stderr, "Error: workspace %q not found\n", target)
		return 1
	}

	if err := config.SaveClientConfig(opts.ConfigPath, cfg); err != nil {
		fmt.Fprintf(stderr, "Failed to persist workspace configuration: %v\n", err)
		return 1
	}

	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"status":  "removed",
			"target":  target,
			"current": cfg.Workspaces,
		})
		return 0
	}

	fmt.Fprintf(stdout, "Successfully removed workspace: %s\n", target)
	return 0
}
