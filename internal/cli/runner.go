package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Sskift/talkintent/internal/config"
)

// PrintUsage prints the main TalkIntent help message.
func PrintUsage(w io.Writer) {
	fmt.Fprintln(w, "TalkIntent — 研发协同感知系统 (Distributed on-site agent probe + central Hub)")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  talkintent <subcommand> [flags]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  hub        Start the central coordination Hub server")
	fmt.Fprintln(w, "  pair       Pair client node with the Hub using an invite code")
	fmt.Fprintln(w, "  daemon     Run the on-site client daemon to answer queries [start|stop|status]")
	fmt.Fprintln(w, "  ask        Submit a natural-language query to a teammate's dev workspace")
	fmt.Fprintln(w, "  workspace  Manage locally monitored workspace repositories [add|list|remove]")
	fmt.Fprintln(w, "  members    List registered team members and online status")
	fmt.Fprintln(w, "  history    Display inbound or outbound query audit history")
	fmt.Fprintln(w, "  llm        Configure or test local LLM provider credentials [show|set|test]")
	fmt.Fprintln(w, "  privacy    Inspect or dry-run sovereign privacy guardrails [init|show|edit-path|test]")
	fmt.Fprintln(w, "  web        Print or launch the Web UI dashboard")
	fmt.Fprintln(w, "  skill      Manage Claude Code skill integration [install|show]")
	fmt.Fprintln(w, "  invite     Generate onboarding invite codes (admin only)")
	fmt.Fprintln(w, "  status     Show local node configuration and workspace status")
	fmt.Fprintln(w, "  version    Display version and runtime information")
}

// Run executes the CLI command against the provided argument list and I/O streams.
// It returns an exit code suitable for os.Exit, but does NOT call os.Exit itself,
// ensuring complete, deterministic testability.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		PrintUsage(stdout)
		return 0
	}

	subcmd := args[0]
	subArgs := args[1:]

	switch subcmd {
	case "hub":
		return runHubCmd(ctx, subArgs, stdout, stderr)
	case "pair":
		return runPairCmd(ctx, subArgs, stdout, stderr)
	case "daemon":
		return runDaemonCmd(ctx, subArgs, stdout, stderr)
	case "ask":
		return runAskCmd(ctx, subArgs, stdout, stderr)
	case "members":
		return runMembersCmd(ctx, subArgs, stdout, stderr)
	case "history":
		return runHistoryCmd(ctx, subArgs, stdout, stderr)
	case "llm":
		return runLLMCmd(ctx, subArgs, stdout, stderr)
	case "workspace":
		return runWorkspaceCmd(subArgs, stdout, stderr)
	case "privacy":
		return runPrivacyCmd(ctx, subArgs, stdout, stderr)
	case "web":
		return runWebCmd(subArgs, stdout, stderr)
	case "skill":
		return runSkillCmd(subArgs, stdout, stderr)
	case "invite":
		return runInviteCmd(ctx, subArgs, stdout, stderr)
	case "status":
		return runStatusCmd(ctx, subArgs, stdout, stderr)
	case "version", "-v", "--version":
		return runVersionCmd(subArgs, stdout, stderr)
	case "help", "-h", "--help":
		PrintUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "Unknown subcommand %q\n\n", subcmd)
		PrintUsage(stderr)
		return 1
	}
}

func runHubCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", ":8080", "HTTP/WS listen address")
	dataDir := fs.String("data-dir", "./data", "Storage data directory for JSONL events")
	adminToken := fs.String("admin-token", "", "Admin token for generating invites (or $TALKINTENT_ADMIN_TOKEN)")
	publicURL := fs.String("public-url", "", "Public URL for invite generation")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := HubOptions{
		Addr:       *addr,
		DataDir:    *dataDir,
		AdminToken: *adminToken,
		PublicURL:  *publicURL,
		JSONOutput: *jsonOut,
	}
	return ExecuteHub(ctx, opts, stdout, stderr)
}

func runPairCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var flagArgs []string
	var posArgs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if !strings.Contains(arg, "=") && (arg == "-hub" || arg == "--hub" ||
				arg == "-code" || arg == "--code" ||
				arg == "-name" || arg == "--name" ||
				arg == "-config" || arg == "--config") {
				if i+1 < len(args) {
					i++
					flagArgs = append(flagArgs, args[i])
				}
			}
		} else {
			posArgs = append(posArgs, arg)
		}
	}

	fs := flag.NewFlagSet("pair", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hubURL := fs.String("hub", "", "TalkIntent Hub URL (e.g. http://hub.example.com:8080)")
	code := fs.String("code", "", "One-time invite code provided by administrator")
	machine := fs.String("name", "", "Machine label (defaults to hostname)")
	cfgPath := fs.String("config", "", "Path to client config.json")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	hubVal := *hubURL
	codeVal := *code

	// Fall back to TALKINTENT_HUB_URL environment variable if --hub flag is empty
	if hubVal == "" {
		if env := os.Getenv(config.EnvTalkIntentHubURL); env != "" {
			hubVal = env
		}
	}

	// Positional arguments support:
	// talkintent pair --hub <url> <invite_code>
	// talkintent pair <invite_code> (with TALKINTENT_HUB_URL)
	// talkintent pair <url> <invite_code>
	// talkintent pair --code <invite_code> <url>
	if codeVal == "" {
		if *hubURL == "" && len(posArgs) >= 2 {
			hubVal = posArgs[0]
			codeVal = posArgs[1]
		} else if len(posArgs) >= 1 {
			codeVal = posArgs[0]
		}
	} else if *hubURL == "" && len(posArgs) >= 1 {
		hubVal = posArgs[0]
	}

	opts := PairOptions{
		HubURL:      hubVal,
		InviteCode:  codeVal,
		MachineName: *machine,
		ConfigPath:  *cfgPath,
		JSONOutput:  *jsonOut,
	}
	return ExecutePair(ctx, opts, stdout, stderr)
}

func runDaemonCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	action := ""
	var flagArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if arg == "-config" || arg == "--config" || arg == "-workspace" || arg == "--workspace" {
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					flagArgs = append(flagArgs, args[i])
				}
			}
		} else {
			if action == "" && (arg == "stop" || arg == "status" || arg == "start") {
				action = arg
			}
		}
	}

	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "Path to client config.json")
	wsPath := fs.String("workspace", "", "Additional workspace directory to monitor")
	detach := fs.Bool("detach", false, "Run daemon detached in background")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	opts := DaemonOptions{
		ConfigPath: *cfgPath,
		Workspace:  *wsPath,
		Detach:     *detach,
		Action:     action,
		JSONOutput: *jsonOut,
	}
	return ExecuteDaemon(ctx, opts, stdout, stderr)
}

func runAskCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("target", "", "Target teammate name or alias")
	to := fs.String("to", "", "Target teammate name or alias (alias for --target)")
	query := fs.String("query", "", "Question regarding the target's dev workspace")
	q := fs.String("q", "", "Question regarding the target's dev workspace (short alias)")
	wait := fs.Bool("wait", true, "Wait for on-site probe response (long-polling)")
	timeout := fs.Int("timeout", 60, "Query timeout in seconds")
	ttl := fs.Int("ttl", 86400, "Query TTL in seconds")
	workspace := fs.String("workspace", "", "Target workspace name on teammate machine")
	cfgPath := fs.String("config", "", "Path to client config.json")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	targetVal := *target
	if targetVal == "" {
		targetVal = *to
	}

	queryVal := *query
	if queryVal == "" {
		queryVal = *q
	}

	// Positional arguments support: talkintent ask "问一下张三现在登录模块怎么样了"
	if queryVal == "" && fs.NArg() > 0 {
		queryVal = strings.Join(fs.Args(), " ")
	}

	cfg, err := config.LoadClientConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "Error: failed to load client config: %v. Have you run 'talkintent pair'?\n", err)
		return 1
	}

	opts := RunAskOptions{
		Target:     targetVal,
		Query:      queryVal,
		Wait:       *wait,
		TimeoutSec: *timeout,
		TTLSec:     *ttl,
		Workspace:  *workspace,
		JSONOutput: *jsonOut,
	}

	return ExecuteAsk(ctx, cfg, opts, stdout, stderr)
}

func runMembersCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("members", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "Path to client config.json")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := MembersOptions{
		ConfigPath: *cfgPath,
		JSONOutput: *jsonOut,
	}
	return ExecuteMembers(ctx, opts, stdout, stderr)
}

func runHistoryCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inbound := fs.Bool("inbound", false, "Show inbound queries answered by local workspaces")
	outbound := fs.Bool("outbound", false, "Show outbound queries submitted to teammates")
	limit := fs.Int("limit", 20, "Maximum records to retrieve")
	offset := fs.Int("offset", 0, "Record offset")
	cfgPath := fs.String("config", "", "Path to client config.json")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := HistoryOptions{
		ConfigPath: *cfgPath,
		Inbound:    *inbound,
		Outbound:   *outbound,
		Limit:      *limit,
		Offset:     *offset,
		JSONOutput: *jsonOut,
	}
	return ExecuteHistory(ctx, opts, stdout, stderr)
}

func runLLMCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	action := "show"
	var flagArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if arg == "-provider" || arg == "--provider" ||
				arg == "-base-url" || arg == "--base-url" ||
				arg == "-api-key" || arg == "--api-key" ||
				arg == "-model" || arg == "--model" ||
				arg == "-max-tokens" || arg == "--max-tokens" ||
				arg == "-temperature" || arg == "--temperature" ||
				arg == "-ca-file" || arg == "--ca-file" ||
				arg == "-tls-server-name" || arg == "--tls-server-name" ||
				arg == "-request-timeout" || arg == "--request-timeout" ||
				arg == "-max-steps" || arg == "--max-steps" ||
				arg == "-config" || arg == "--config" {
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					flagArgs = append(flagArgs, args[i])
				}
			}
		} else {
			if action == "show" && (arg == "set" || arg == "show" || arg == "get" || arg == "test") {
				action = arg
			}
		}
	}

	fs := flag.NewFlagSet("llm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := fs.String("provider", "", "LLM provider: 'openai' or 'anthropic'")
	baseURL := fs.String("base-url", "", "API Base URL endpoint")
	apiKey := fs.String("api-key", "", "API Key")
	model := fs.String("model", "", "Model name (e.g. gpt-4o, claude-3-7-sonnet)")
	maxTokens := fs.Int("max-tokens", 0, "Max response tokens")
	temperature := fs.Float64("temperature", 0.0, "Sampling temperature")
	caFile := fs.String("ca-file", "", "Path to custom CA certificate PEM bundle")
	tlsServerName := fs.String("tls-server-name", "", "SNI / TLS server name override")
	insecure := fs.Bool("insecure-skip-verify", false, "Explicitly skip TLS certificate verification")
	timeoutSec := fs.Int("request-timeout", 0, "HTTP request timeout in seconds")
	maxSteps := fs.Int("max-steps", 0, "Maximum tool calling iterations")
	cfgPath := fs.String("config", "", "Path to client config.json")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	opts := LLMOptions{
		ConfigPath:         *cfgPath,
		Subcommand:         action,
		Provider:           *provider,
		BaseURL:            *baseURL,
		APIKey:             *apiKey,
		Model:              *model,
		MaxTokens:          *maxTokens,
		Temperature:        *temperature,
		CAFile:             *caFile,
		TLSServerName:      *tlsServerName,
		InsecureSkipVerify: *insecure,
		RequestTimeoutSec:  *timeoutSec,
		MaxSteps:           *maxSteps,
		JSONOutput:         *jsonOut,
	}
	return ExecuteLLM(ctx, opts, stdout, stderr)
}

func runWorkspaceCmd(args []string, stdout, stderr io.Writer) int {
	action := "list"
	var posArgs []string
	var flagArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if arg == "-config" || arg == "--config" || arg == "-name" || arg == "--name" {
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					flagArgs = append(flagArgs, args[i])
				}
			}
		} else {
			if action == "list" && (arg == "list" || arg == "ls" || arg == "add" || arg == "remove" || arg == "rm") {
				action = arg
			} else {
				posArgs = append(posArgs, arg)
			}
		}
	}

	fs := flag.NewFlagSet("workspace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "Path to client config.json")
	name := fs.String("name", "", "Workspace custom name")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	target := ""
	if len(posArgs) > 0 {
		target = posArgs[0]
	}
	nameVal := *name
	if nameVal == "" && len(posArgs) > 1 {
		nameVal = posArgs[1]
	}

	opts := WorkspaceOptions{
		ConfigPath: *cfgPath,
		Subcommand: action,
		Path:       target,
		Target:     target,
		Name:       nameVal,
		JSONOutput: *jsonOut,
	}
	return ExecuteWorkspace(opts, stdout, stderr)
}

func runPrivacyCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	action := "show"
	var flagArgs []string
	var posArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if arg == "-config" || arg == "--config" ||
				arg == "-workspace" || arg == "--workspace" ||
				arg == "-path" || arg == "--path" {
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					flagArgs = append(flagArgs, args[i])
				}
			}
		} else {
			if action == "show" && (arg == "init" || arg == "show" || arg == "edit-path" || arg == "test") {
				action = arg
			} else {
				posArgs = append(posArgs, arg)
			}
		}
	}

	fs := flag.NewFlagSet("privacy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "Path to client config.json")
	workspace := fs.String("workspace", "", "Target workspace name")
	path := fs.String("path", "", "Privacy prompt file destination path")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	query := strings.Join(posArgs, " ")

	opts := PrivacyOptions{
		ConfigPath: *cfgPath,
		Subcommand: action,
		Query:      query,
		Workspace:  *workspace,
		Path:       *path,
		JSONOutput: *jsonOut,
	}
	return ExecutePrivacy(ctx, opts, stdout, stderr)
}

func runWebCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "Path to client config.json")
	open := fs.Bool("open", false, "Open dashboard in system default browser")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := WebOptions{
		ConfigPath: *cfgPath,
		Open:       *open,
		JSONOutput: *jsonOut,
	}
	return ExecuteWeb(opts, stdout, stderr)
}

func runSkillCmd(args []string, stdout, stderr io.Writer) int {
	action := "install"
	var flagArgs []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if arg == "-dir" || arg == "--dir" {
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					flagArgs = append(flagArgs, args[i])
				}
			}
		} else {
			if action == "install" && (arg == "install" || arg == "show" || arg == "cat") {
				action = arg
			}
		}
	}

	fs := flag.NewFlagSet("skill", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "Custom target directory for skill installation")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	opts := SkillOptions{
		Subcommand: action,
		Dir:        *dir,
		JSONOutput: *jsonOut,
	}
	return ExecuteSkill(opts, stdout, stderr)
}

func runInviteCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("invite", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hubURL := fs.String("hub", "", "Hub URL")
	adminToken := fs.String("admin-token", "", "Admin authentication token")
	name := fs.String("name", "", "Target member name (required)")
	alias := fs.String("alias", "", "Comma-separated member aliases")
	expires := fs.Int("expires-hours", 72, "Invite expiration duration in hours")
	cfgPath := fs.String("config", "", "Path to client config.json")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	nameVal := *name
	if nameVal == "" && fs.NArg() > 0 {
		nameVal = fs.Arg(0)
	}

	var aliases []string
	if *alias != "" {
		for _, a := range strings.Split(*alias, ",") {
			trimmed := strings.TrimSpace(a)
			if trimmed != "" {
				aliases = append(aliases, trimmed)
			}
		}
	}

	opts := InviteOptions{
		ConfigPath:     *cfgPath,
		HubURL:         *hubURL,
		AdminToken:     *adminToken,
		TargetName:     nameVal,
		Aliases:        aliases,
		ExpiresInHours: *expires,
		JSONOutput:     *jsonOut,
	}
	return ExecuteInvite(ctx, opts, stdout, stderr)
}

func runStatusCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "Path to client config.json")
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := StatusOptions{
		ConfigPath: *cfgPath,
		JSONOutput: *jsonOut,
	}
	return ExecuteStatus(ctx, opts, stdout, stderr)
}

func runVersionCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "Output in JSON format")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	return ExecuteVersion(*jsonOut, stdout)
}
