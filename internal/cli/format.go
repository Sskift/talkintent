package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/protocol"
)

// PrintJSON writes an indented JSON representation of v to w.
func PrintJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// FormatTime formats a millisecond timestamp for human display.
func FormatTime(ms int64) string {
	if ms <= 0 {
		return "never"
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

// PrintMembersTable formats a list of team members into a clean table.
func PrintMembersTable(w io.Writer, members []protocol.MemberInfo) {
	if len(members) == 0 {
		fmt.Fprintln(w, "No registered team members found.")
		return
	}

	fmt.Fprintf(w, "TEAM MEMBERS (%d)\n", len(members))
	fmt.Fprintln(w, strings.Repeat("-", 80))
	fmt.Fprintf(w, "%-18s %-10s %-18s %-8s %-19s %s\n",
		"ID", "NAME", "ALIASES", "STATUS", "LAST SEEN", "WORKSPACES")
	fmt.Fprintln(w, strings.Repeat("-", 80))

	for _, m := range members {
		status := "OFFLINE"
		if m.Online {
			status = "ONLINE"
		}
		aliases := strings.Join(m.Aliases, ", ")
		if aliases == "" {
			aliases = "-"
		}
		if len(aliases) > 18 {
			aliases = aliases[:15] + "..."
		}
		lastSeen := FormatTime(m.LastSeenAt)
		workspaces := strings.Join(m.Workspaces, ", ")
		if workspaces == "" {
			workspaces = "-"
		}

		fmt.Fprintf(w, "%-18s %-10s %-18s %-8s %-19s %s\n",
			m.ID, m.Name, aliases, status, lastSeen, workspaces)
	}
	fmt.Fprintln(w, strings.Repeat("-", 80))
}

// PrintAuditTable formats a list of audit entries.
func PrintAuditTable(w io.Writer, entries []protocol.AuditLogEntry, title string) {
	if len(entries) == 0 {
		fmt.Fprintf(w, "No audit entries found (%s).\n", title)
		return
	}

	fmt.Fprintf(w, "AUDIT HISTORY — %s (%d entries)\n", strings.ToUpper(title), len(entries))
	fmt.Fprintln(w, strings.Repeat("=", 80))

	for i, e := range entries {
		t := FormatTime(e.Timestamp)
		fmt.Fprintf(w, "[%d] %s | %s -> %s | Status: %s (%dms)\n",
			i+1, t, e.AskerName, e.TargetMemberName, e.Status, e.DurationMS)
		fmt.Fprintf(w, "    Query:  %s\n", e.Query)
		if e.Answer != "" {
			ans := e.Answer
			if len(ans) > 200 {
				ans = ans[:197] + "..."
			}
			fmt.Fprintf(w, "    Answer: %s\n", ans)
		}
		if len(e.ToolsUsed) > 0 {
			fmt.Fprintf(w, "    Tools:  %s\n", strings.Join(e.ToolsUsed, ", "))
		}
		fmt.Fprintln(w, strings.Repeat("-", 80))
	}
}

// PrintWorkspacesTable formats configured workspaces into a clean list.
func PrintWorkspacesTable(w io.Writer, workspaces []config.WorkspaceConfig) {
	fmt.Fprintf(w, "Configured Workspaces (%d):\n", len(workspaces))
	if len(workspaces) == 0 {
		fmt.Fprintln(w, "  (No workspaces configured yet. Use 'talkintent workspace add <path>' to add one)")
		return
	}
	for i, ws := range workspaces {
		promptInfo := ""
		if ws.PrivacyPromptPath != "" {
			promptInfo = fmt.Sprintf(" [prompt: %s]", ws.PrivacyPromptPath)
		}
		fmt.Fprintf(w, "  [%d] %s (%s) -> %s%s\n", i+1, ws.Name, ws.ID, ws.RootPath, promptInfo)
	}
}
