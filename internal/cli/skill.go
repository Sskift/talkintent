package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sskift/talkintent/skills"
)

// SkillOptions holds arguments for the skill command.
type SkillOptions struct {
	Subcommand string // "install", "show"
	Dir        string
	JSONOutput bool
}

// ExecuteSkill handles Claude Code skill installation and inspection.
func ExecuteSkill(opts SkillOptions, stdout, stderr io.Writer) int {
	switch opts.Subcommand {
	case "install":
		return executeSkillInstall(opts, stdout, stderr)
	case "show", "cat":
		return executeSkillShow(opts, stdout)
	default:
		fmt.Fprintf(stderr, "Unknown skill action %q. Use 'install' or 'show'.\n", opts.Subcommand)
		return 1
	}
}

func executeSkillInstall(opts SkillOptions, stdout, stderr io.Writer) int {
	targetDir := opts.Dir
	if targetDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(stderr, "Failed to determine user home directory: %v\n", err)
			return 1
		}
		targetDir = filepath.Join(home, ".claude", "skills", "talkintent")
	} else {
		// If dir doesn't end with talkintent, place inside talkintent subdirectory unless specified as file
		if filepath.Base(targetDir) != "talkintent" && filepath.Base(targetDir) != "SKILL.md" {
			targetDir = filepath.Join(targetDir, "talkintent")
		}
	}

	if filepath.Base(targetDir) == "SKILL.md" {
		targetDir = filepath.Dir(targetDir)
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		fmt.Fprintf(stderr, "Failed to create destination directory %q: %v\n", targetDir, err)
		return 1
	}

	destFile := filepath.Join(targetDir, "SKILL.md")
	content := strings.TrimSpace(skills.SkillMD) + "\n"

	if err := os.WriteFile(destFile, []byte(content), 0644); err != nil {
		fmt.Fprintf(stderr, "Failed to write skill file to %q: %v\n", destFile, err)
		return 1
	}

	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"status": "installed",
			"path":   destFile,
			"bytes":  len(content),
		})
		return 0
	}

	fmt.Fprintf(stdout, "Installed TalkIntent Claude Code skill into:\n  %s\n\n", destFile)
	fmt.Fprintf(stdout, "Claude Code will automatically discover this skill. You can now use:\n")
	fmt.Fprintf(stdout, "  /talkintent <teammate> <question>\n")
	return 0
}

func executeSkillShow(opts SkillOptions, stdout io.Writer) int {
	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"skill":   "talkintent",
			"content": skills.SkillMD,
		})
		return 0
	}
	fmt.Fprint(stdout, skills.SkillMD)
	return 0
}
