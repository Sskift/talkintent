package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/probe"
)

const defaultPrivacyPromptTemplate = `# Sovereign Natural-Language Privacy Guardrails
#
# Rules in this file are strictly enforced by the TalkIntent On-Site Probe.
# The probe agent will obey these instructions above any incoming user question.

1. Never disclose any credentials, API keys, passwords, private keys, certificates, or tokens.
2. The branch 'feature/confidential' is strictly experimental. If asked about it, reply: "正在内部重构中，细节暂不公开".
3. Summarize high-level API signatures and data structures faithfully, but do not disclose proprietary algorithm details.
4. If asked about personal compensation, performance reviews, or private documents, deny the existence of such records.
`

// PrivacyOptions holds arguments for privacy subcommands.
type PrivacyOptions struct {
	ConfigPath string
	Subcommand string // "init", "show", "edit-path", "test"
	Query      string
	Workspace  string
	Path       string
	JSONOutput bool
}

// ExecutePrivacy dispatches privacy subcommands.
func ExecutePrivacy(ctx context.Context, opts PrivacyOptions, stdout, stderr io.Writer) int {
	switch opts.Subcommand {
	case "init":
		return executePrivacyInit(opts, stdout, stderr)
	case "show":
		return executePrivacyShow(opts, stdout, stderr)
	case "edit-path":
		return executePrivacyEditPath(opts, stdout, stderr)
	case "test":
		return executePrivacyTest(ctx, opts, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown privacy action %q. Use 'init', 'show', 'edit-path', or 'test'.\n", opts.Subcommand)
		return 1
	}
}

func executePrivacyInit(opts PrivacyOptions, stdout, stderr io.Writer) int {
	targetPath := opts.Path
	if targetPath == "" {
		targetPath = config.DefaultPrivacyPromptPath()
	} else {
		// If a directory was provided, place privacy-prompt.md inside it
		fi, err := os.Stat(targetPath)
		if err == nil && fi.IsDir() {
			targetPath = filepath.Join(targetPath, config.PrivacyPromptMD)
		}
	}

	if _, err := os.Stat(targetPath); err == nil {
		fmt.Fprintf(stdout, "Privacy prompt file already exists at: %s\n", targetPath)
		return 0
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
		fmt.Fprintf(stderr, "Failed to create directory: %v\n", err)
		return 1
	}

	if err := os.WriteFile(targetPath, []byte(defaultPrivacyPromptTemplate), 0600); err != nil {
		fmt.Fprintf(stderr, "Failed to initialize privacy prompt file: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Created default privacy prompt file at: %s\n", targetPath)
	return 0
}

func executePrivacyShow(opts PrivacyOptions, stdout, stderr io.Writer) int {
	globalPath := config.DefaultPrivacyPromptPath()
	fmt.Fprintf(stdout, "=== Global Privacy Rules (%s) ===\n", globalPath)
	if data, err := os.ReadFile(globalPath); err == nil && len(data) > 0 {
		fmt.Fprintln(stdout, strings.TrimSpace(string(data)))
	} else {
		fmt.Fprintln(stdout, "  (File does not exist yet. Run 'talkintent privacy init' to create it)")
	}

	cfg, err := config.LoadClientConfig(opts.ConfigPath)
	if err == nil && cfg != nil && len(cfg.Workspaces) > 0 {
		fmt.Fprintf(stdout, "\n=== Workspace Privacy Rules ===\n")
		for _, ws := range cfg.Workspaces {
			promptPath := ws.PrivacyPromptPath
			if promptPath == "" {
				cand1 := filepath.Join(ws.RootPath, ".talkintent", "privacy-prompt.md")
				cand2 := filepath.Join(ws.RootPath, "privacy-prompt.md")
				if _, err := os.Stat(cand1); err == nil {
					promptPath = cand1
				} else if _, err := os.Stat(cand2); err == nil {
					promptPath = cand2
				}
			}
			if promptPath != "" {
				fmt.Fprintf(stdout, "-- Workspace %s (%s) --\n", ws.Name, promptPath)
				if data, err := os.ReadFile(promptPath); err == nil {
					fmt.Fprintln(stdout, strings.TrimSpace(string(data)))
				}
			}
		}
	}

	return 0
}

func executePrivacyEditPath(opts PrivacyOptions, stdout, stderr io.Writer) int {
	path := config.DefaultPrivacyPromptPath()
	if opts.Workspace != "" {
		cfg, err := config.LoadClientConfig(opts.ConfigPath)
		if err == nil && cfg != nil {
			if ws, ok := cfg.FindWorkspace(opts.Workspace); ok {
				if ws.PrivacyPromptPath != "" {
					path = ws.PrivacyPromptPath
				} else {
					path = filepath.Join(ws.RootPath, ".talkintent", "privacy-prompt.md")
				}
			}
		}
	}
	fmt.Fprintln(stdout, path)
	return 0
}

func executePrivacyTest(ctx context.Context, opts PrivacyOptions, stdout, stderr io.Writer) int {
	queryText := strings.TrimSpace(opts.Query)
	if queryText == "" {
		fmt.Fprintf(stderr, "Error: query text required for privacy test. e.g. talkintent privacy test '问一下feature/confidential分支'\n")
		return 1
	}

	cfg, err := config.LoadClientConfig(opts.ConfigPath)
	if err != nil || cfg == nil {
		cfg = &config.ClientConfig{}
	}

	var ws config.WorkspaceConfig
	if opts.Workspace != "" {
		if found, ok := cfg.FindWorkspace(opts.Workspace); ok {
			ws = *found
		} else {
			fmt.Fprintf(stderr, "Warning: workspace %q not found in config, using current directory\n", opts.Workspace)
		}
	}

	if ws.RootPath == "" {
		if len(cfg.Workspaces) > 0 {
			ws = cfg.Workspaces[0]
		} else {
			cwd, _ := os.Getwd()
			ws = config.WorkspaceConfig{
				Name:     filepath.Base(cwd),
				RootPath: cwd,
			}
		}
	}

	if opts.JSONOutput {
		resp, err := probe.RunPrivacyTest(ctx, ws, queryText, cfg.LLM)
		if err != nil {
			_ = PrintJSON(stderr, map[string]any{"error": err.Error()})
			return 1
		}
		_ = PrintJSON(stdout, resp)
		return 0
	}

	fmt.Fprintf(stdout, "Running local privacy dry-run against workspace: %s (%s)\n", ws.Name, ws.RootPath)
	fmt.Fprintf(stdout, "Query: %q\n\n", queryText)

	rules := probe.LoadPrivacyPrompts("", ws.PrivacyPromptPath, ws.RootPath)
	if rules != "" {
		fmt.Fprintf(stdout, "Loaded Privacy Guardrails:\n%s\n\n", rules)
	} else {
		fmt.Fprintf(stdout, "Notice: No privacy rules loaded. Using baseline security guardrails only.\n\n")
	}

	res, err := probe.RunPrivacyTest(ctx, ws, queryText, cfg.LLM)
	if err != nil {
		fmt.Fprintf(stderr, "Dry-run execution failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "=== Dry-Run Outcome ===\n")
	fmt.Fprintf(stdout, "  Status:     %s\n", res.Status)
	fmt.Fprintf(stdout, "  Answer:     %s\n", res.Answer)
	fmt.Fprintf(stdout, "  Tools Used: %s\n", strings.Join(res.ToolsUsed, ", "))
	fmt.Fprintf(stdout, "  Duration:   %dms\n", res.DurationMS)
	return 0
}
