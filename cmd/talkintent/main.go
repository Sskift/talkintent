package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Sskift/talkintent/internal/cli"
	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/daemon"
	"github.com/Sskift/talkintent/internal/hub"
	"github.com/Sskift/talkintent/internal/probe"
	"github.com/Sskift/talkintent/internal/store"
	"github.com/Sskift/talkintent/web"
)

var (
	Version   = "1.0.0"
	BuildTime = "2026-09-20"
)

const EmbeddedSkillMD = `# TalkIntent Claude Code Skill

Ask a teammate's live dev workspace questions asynchronously without interrupting them.

## Usage

` + "```bash\n/talkintent <teammate-name-or-alias> <question>\n```" + `

Examples:
- ` + "`/talkintent 张三 现在登录模块重构得怎么样了，有没有新的结构体定义？`" + `
- ` + "`/talkintent lisi 本地服务跑在什么端口上？`" + `
- ` + "`/talkintent wangwu feature/auth 分支最近改了哪些文件？`" + `

## Execution

When invoked, the skill runs:
` + "```bash\ntalkintent ask --target \"<target>\" --query \"<query>\" --wait\n```" + `

The Hub dispatches the query to the teammate's on-site background daemon, which runs a lightweight LLM probe agent sandboxed in their local workspace. The probe checks live git diffs, branch state, recent edits, configs, and open ports under that teammate's sovereign natural-language privacy guardrails (` + "`privacy-prompt.md`" + `). Only the synthesized final answer leaves their machine.
`

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "hub":
		err = runHub(ctx, args)
	case "pair":
		err = runPair(ctx, args)
	case "daemon":
		err = runDaemon(ctx, args)
	case "ask":
		err = runAsk(ctx, args)
	case "members":
		err = runMembers(ctx, args)
	case "history":
		err = runHistory(ctx, args)
	case "llm":
		err = runLLM(ctx, args)
	case "workspace":
		err = runWorkspace(ctx, args)
	case "privacy":
		err = runPrivacy(ctx, args)
	case "web":
		err = runWeb(ctx, args)
	case "skill":
		err = runSkill(ctx, args)
	case "status":
		err = runStatus(ctx, args)
	case "version", "-v", "--version":
		runVersion()
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand %q\n\n", cmd)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("TalkIntent — 研发协同感知系统 (Distributed on-site agent probe + central Hub)")
	fmt.Println("\nUsage:")
	fmt.Println("  talkintent <subcommand> [flags]")
	fmt.Println("\nSubcommands:")
	fmt.Println("  hub        Start the central coordination Hub server")
	fmt.Println("  pair       Pair client node with the Hub using an invite code")
	fmt.Println("  daemon     Run the on-site client daemon to answer queries")
	fmt.Println("  ask        Submit a natural-language query to a teammate's dev workspace")
	fmt.Println("  workspace  Manage locally monitored workspace repositories [add|list|remove]")
	fmt.Println("  members    List registered team members and online status")
	fmt.Println("  history    Display inbound or outbound query audit history")
	fmt.Println("  llm        Configure or test local LLM provider credentials")
	fmt.Println("  privacy    Inspect or dry-run local natural-language privacy guardrails")
	fmt.Println("  web        Print or launch the Web UI login dashboard")
	fmt.Println("  skill      Manage Claude Code skill integration")
	fmt.Println("  status     Show local node configuration and workspace status")
	fmt.Println("  version    Display version and runtime information")
}

func runHub(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("hub", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "HTTP/WS listen address")
	dataDir := fs.String("data-dir", "./data", "Storage data directory for JSONL events")
	adminToken := fs.String("admin-token", "", "Admin token for generating invites (or $TALKINTENT_ADMIN_TOKEN)")
	_ = fs.Parse(args)

	token := *adminToken
	if token == "" {
		token = os.Getenv("TALKINTENT_ADMIN_TOKEN")
	}

	cfg := &config.HubConfig{
		Addr:       *addr,
		DataDir:    *dataDir,
		AdminToken: token,
	}

	st, err := store.NewJSONLStore(*dataDir)
	if err != nil {
		return fmt.Errorf("failed to initialize store: %w", err)
	}
	defer st.Close()

	srv, err := hub.NewServer(cfg, st, web.StaticFiles, slog.Default())
	if err != nil {
		return fmt.Errorf("failed to create hub server: %w", err)
	}

	return srv.Start(ctx)
}

func runPair(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pair", flag.ExitOnError)
	hubURL := fs.String("hub", "", "TalkIntent Hub URL (e.g. http://hub.example.com:8080)")
	code := fs.String("code", "", "One-time invite code provided by administrator")
	machineName := fs.String("name", "", "Machine label (defaults to hostname)")
	_ = fs.Parse(args)

	return cli.RunPair(ctx, *hubURL, *code, *machineName)
}

func runDaemon(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to client config.json (default: ~/.talkintent/config.json)")
	workspace := fs.String("workspace", "", "Additional workspace directory to monitor")
	_ = fs.Parse(args)

	cfg, err := config.LoadClientConfig(*configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w. Run 'talkintent pair' first", err)
	}

	if *workspace != "" {
		norm, err := config.NormalizeWorkspacePath(*workspace)
		if err != nil {
			return err
		}
		cfg.Workspaces = append(cfg.Workspaces, config.WorkspaceConfig{
			ID:       fmt.Sprintf("ws_%d", time.Now().UnixNano()),
			Name:     filepath.Base(norm),
			RootPath: norm,
		})
	}

	agent := probe.NewDefaultAgent(nil)
	d, err := daemon.NewClientDaemon(cfg, agent, slog.Default())
	if err != nil {
		return fmt.Errorf("failed to initialize daemon: %w", err)
	}

	return d.Start(ctx)
}

func runAsk(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	target := fs.String("target", "", "Teammate name or alias (e.g. '张三' or 'zhangsan')")
	query := fs.String("query", "", "Question regarding the target's dev workspace state")
	wait := fs.Bool("wait", true, "Wait for on-site probe response (long-polling)")
	_ = fs.Parse(args)

	targetVal := *target
	queryVal := *query

	// If user ran: talkintent ask "问一下张三现在登录模块怎么样了"
	if targetVal == "" && len(fs.Args()) > 0 {
		queryVal = strings.Join(fs.Args(), " ")
		// Attempt to extract target if format starts with "问一下" or "问下"
		clean := strings.TrimPrefix(queryVal, "问一下")
		clean = strings.TrimPrefix(clean, "问下")
		fields := strings.Fields(clean)
		if len(fields) > 0 {
			targetVal = fields[0]
		} else {
			targetVal = queryVal
		}
	}

	if targetVal == "" && queryVal != "" {
		targetVal = queryVal
	}

	if queryVal == "" {
		return fmt.Errorf("query text is required (e.g. talkintent ask --target 张三 --query '现在进度如何' OR talkintent ask '问一下张三现在进度如何')")
	}

	return cli.RunAsk(ctx, targetVal, queryVal, *wait)
}

func runWorkspace(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: talkintent workspace [add|list|remove] [path]")
	}
	action := args[0]
	cfg, err := config.LoadClientConfig("")
	if err != nil {
		return fmt.Errorf("failed to load client config: %w. Run 'talkintent pair' first", err)
	}

	switch action {
	case "list", "ls":
		fmt.Printf("Configured Workspaces (%d):\n", len(cfg.Workspaces))
		if len(cfg.Workspaces) == 0 {
			fmt.Println("  (No workspaces configured yet. Use 'talkintent workspace add <path>' to add one)")
			return nil
		}
		for i, ws := range cfg.Workspaces {
			fmt.Printf("  [%d] %s -> %s\n", i+1, ws.Name, ws.RootPath)
		}
		return nil

	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: talkintent workspace add <directory_path> [name]")
		}
		dirPath := args[1]
		norm, err := config.NormalizeWorkspacePath(dirPath)
		if err != nil {
			return err
		}
		name := filepath.Base(norm)
		if len(args) >= 3 {
			name = args[2]
		}
		for _, ws := range cfg.Workspaces {
			if ws.RootPath == norm {
				fmt.Printf("Workspace already exists: %s (%s)\n", ws.Name, ws.RootPath)
				return nil
			}
		}
		cfg.Workspaces = append(cfg.Workspaces, config.WorkspaceConfig{
			ID:       fmt.Sprintf("ws_%d", time.Now().UnixNano()),
			Name:     name,
			RootPath: norm,
		})
		if err := config.SaveClientConfig("", cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		fmt.Printf("Successfully added workspace: %s (%s)\n", name, norm)
		return nil

	case "remove", "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: talkintent workspace remove <name_or_path>")
		}
		target := args[1]
		var filtered []config.WorkspaceConfig
		found := false
		for _, ws := range cfg.Workspaces {
			if ws.Name == target || ws.RootPath == target || ws.ID == target {
				found = true
			} else {
				filtered = append(filtered, ws)
			}
		}
		if !found {
			return fmt.Errorf("workspace %q not found", target)
		}
		cfg.Workspaces = filtered
		if err := config.SaveClientConfig("", cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		fmt.Printf("Successfully removed workspace: %s\n", target)
		return nil

	default:
		return fmt.Errorf("unknown workspace action %q (use add, list, or remove)", action)
	}
}

func runMembers(ctx context.Context, args []string) error {
	cfg, err := config.LoadClientConfig("")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	fmt.Printf("Connected Hub: %s\n", cfg.HubURL)
	fmt.Println("Fetching member directory... (feature implemented in WP6)")
	return nil
}

func runHistory(ctx context.Context, args []string) error {
	fmt.Println("Query audit history viewer (feature implemented in WP6)")
	return nil
}

func runLLM(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("llm", flag.ExitOnError)
	provider := fs.String("provider", "", "LLM provider: 'openai' or 'anthropic'")
	baseURL := fs.String("base-url", "", "API Base URL endpoint")
	apiKey := fs.String("api-key", "", "API Key")
	model := fs.String("model", "", "Model name (e.g. gpt-4o, claude-3-7-sonnet)")
	caFile := fs.String("ca-file", "", "Path to custom CA certificate PEM bundle")
	tlsServerName := fs.String("tls-server-name", "", "SNI / TLS server name override when dialing gateway by IP")
	insecureSkipVerify := fs.Bool("insecure-skip-verify", false, "Explicitly skip TLS certificate verification (logs warning)")
	test := fs.Bool("test", false, "Test LLM connection and tool support")
	_ = fs.Parse(args)

	cfg, err := config.LoadClientConfig("")
	if err != nil {
		cfg = &config.ClientConfig{}
	}

	if *provider != "" {
		cfg.LLM.Provider = *provider
	}
	if *baseURL != "" {
		cfg.LLM.BaseURL = *baseURL
	}
	if *apiKey != "" {
		cfg.LLM.APIKey = *apiKey
	}
	if *model != "" {
		cfg.LLM.Model = *model
	}
	if *caFile != "" {
		cfg.LLM.CAFile = *caFile
	}
	if *tlsServerName != "" {
		cfg.LLM.TLSServerName = *tlsServerName
	}
	if *insecureSkipVerify {
		cfg.LLM.InsecureSkipVerify = true
	}

	if *provider != "" || *baseURL != "" || *apiKey != "" || *model != "" || *caFile != "" || *tlsServerName != "" || *insecureSkipVerify {
		if err := config.SaveClientConfig("", cfg); err != nil {
			return err
		}
		fmt.Println("Local LLM credentials updated successfully in ~/.talkintent/config.json (0600).")
	}

	if *test {
		fmt.Printf("Testing LLM provider: %s (endpoint: %s, model: %s, ca_file: %s, tls_server_name: %s, insecure: %v)...\n",
			cfg.LLM.Provider, cfg.LLM.BaseURL, cfg.LLM.Model, cfg.LLM.CAFile, cfg.LLM.TLSServerName, cfg.LLM.InsecureSkipVerify)
	}
	return nil
}

func runPrivacy(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("privacy", flag.ExitOnError)
	testQuery := fs.String("test", "", "Dry-run a question against local workspace and privacy rules")
	_ = fs.Parse(args)

	if *testQuery != "" {
		return cli.RunPrivacyTest(ctx, *testQuery)
	}

	globalPath := config.DefaultPrivacyPromptPath()
	fmt.Printf("Global Privacy Rules file: %s\n", globalPath)
	if _, err := os.Stat(globalPath); os.IsNotExist(err) {
		fmt.Println("  (File does not exist yet. Create it to define sovereign privacy rules.)")
	} else {
		fmt.Println("  (File active and will be injected into probe guardrails.)")
	}
	return nil
}

func runWeb(ctx context.Context, args []string) error {
	cfg, err := config.LoadClientConfig("")
	if err != nil {
		return fmt.Errorf("client not paired: %w", err)
	}
	webURL := fmt.Sprintf("%s/web#token=%s", strings.TrimRight(cfg.HubURL, "/"), cfg.Token)
	fmt.Printf("Web Dashboard Login URL:\n\n  %s\n\n", webURL)
	return nil
}

func runSkill(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "install" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		targetDir := filepath.Join(home, ".claude", "skills", "talkintent")
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return fmt.Errorf("failed to create skill directory: %w", err)
		}
		targetFile := filepath.Join(targetDir, "SKILL.md")
		if err := os.WriteFile(targetFile, []byte(EmbeddedSkillMD), 0644); err != nil {
			return fmt.Errorf("failed to write SKILL.md: %w", err)
		}
		fmt.Printf("Installed Claude Code skill into %s\n", targetFile)
		fmt.Println("Claude Code skill installed successfully. Run '/talkintent' inside Claude Code.")
		return nil
	}
	fmt.Println("Usage: talkintent skill install")
	return nil
}

func runStatus(ctx context.Context, args []string) error {
	fmt.Printf("TalkIntent Status\n")
	fmt.Printf("  Home:    %s\n", config.GetTalkIntentHome())
	fmt.Printf("  Config:  %s\n", config.DefaultClientConfigPath())
	cfg, err := config.LoadClientConfig("")
	if err != nil {
		fmt.Printf("  Pairing: Not paired (run 'talkintent pair')\n")
		return nil
	}
	fmt.Printf("  Member:  %s (ID: %s)\n", cfg.MemberName, cfg.MemberID)
	fmt.Printf("  Hub URL: %s\n", cfg.HubURL)
	fmt.Printf("  Machine: %s\n", cfg.MachineName)
	fmt.Printf("  Workspaces (%d):\n", len(cfg.Workspaces))
	for _, ws := range cfg.Workspaces {
		fmt.Printf("    - %s (%s)\n", ws.Name, ws.RootPath)
	}
	return nil
}

func runVersion() {
	fmt.Printf("talkintent version %s (%s) %s/%s\n", Version, BuildTime, runtime.GOOS, runtime.GOARCH)
}
