package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Sskift/talkintent/internal/config"
	"github.com/Sskift/talkintent/internal/protocol"
)

// ExtractTarget extracts the target member name or alias from natural language query text
// by matching registered member names and aliases against the text.
// It selects the longest matching name or alias across all members.
// If multiple distinct members match with the same maximum length, ambiguous is true.
func ExtractTarget(queryText string, members []protocol.MemberInfo) (target string, matchedAlias string, ambiguous bool, candidates []protocol.CandidateMember) {
	if strings.TrimSpace(queryText) == "" || len(members) == 0 {
		return "", "", false, nil
	}

	lowerQuery := strings.ToLower(queryText)

	type memberMatch struct {
		member       protocol.MemberInfo
		matchedAlias string
		matchLen     int // rune length
	}

	bestMatchesByMember := make(map[string]memberMatch)

	for _, m := range members {
		bestLen := 0
		bestAlias := ""

		// Check member canonical name
		name := strings.TrimSpace(m.Name)
		if name != "" {
			lowerName := strings.ToLower(name)
			if strings.Contains(lowerQuery, lowerName) {
				runeLen := utf8.RuneCountInString(name)
				if runeLen > bestLen {
					bestLen = runeLen
					bestAlias = name
				}
			}
		}

		// Check member aliases
		for _, alias := range m.Aliases {
			aliasTrim := strings.TrimSpace(alias)
			if aliasTrim == "" {
				continue
			}
			lowerAlias := strings.ToLower(aliasTrim)
			if strings.Contains(lowerQuery, lowerAlias) {
				runeLen := utf8.RuneCountInString(aliasTrim)
				if runeLen > bestLen {
					bestLen = runeLen
					bestAlias = aliasTrim
				}
			}
		}

		if bestLen > 0 {
			bestMatchesByMember[m.ID] = memberMatch{
				member:       m,
				matchedAlias: bestAlias,
				matchLen:     bestLen,
			}
		}
	}

	if len(bestMatchesByMember) == 0 {
		return "", "", false, nil
	}

	// Find maximum match length
	maxLen := 0
	for _, match := range bestMatchesByMember {
		if match.matchLen > maxLen {
			maxLen = match.matchLen
		}
	}

	// Collect top matches
	var top []memberMatch
	for _, match := range bestMatchesByMember {
		if match.matchLen == maxLen {
			top = append(top, match)
		}
	}

	// Sort deterministically by member ID
	sort.Slice(top, func(i, j int) bool {
		return top[i].member.ID < top[j].member.ID
	})

	if len(top) == 1 {
		return top[0].member.Name, top[0].matchedAlias, false, nil
	}

	// Ambiguous matches across multiple members
	for _, m := range top {
		candidates = append(candidates, protocol.CandidateMember{
			ID:           m.member.ID,
			Name:         m.member.Name,
			MatchedAlias: m.matchedAlias,
		})
	}

	return "", "", true, candidates
}

// FetchMembers retrieves the member directory from the Hub.
func FetchMembers(ctx context.Context, hubURL, token string) ([]protocol.MemberInfo, error) {
	url := strings.TrimRight(hubURL, "/") + "/api/v1/members"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch members: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp protocol.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		return nil, fmt.Errorf("failed to fetch members (HTTP %d): %s", resp.StatusCode, errResp.Error.Message)
	}

	var listResp protocol.MemberListResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, fmt.Errorf("failed to decode members response: %w", err)
	}
	return listResp.Members, nil
}

// RunAskOptions encapsulates options for executing an ask command.
type RunAskOptions struct {
	Target     string
	Query      string
	Workspace  string
	TimeoutSec int
	TTLSec     int
	Wait       bool
	JSONOutput bool
}

// ExecuteAsk runs the complete query submission, natural language resolution,
// and long-polling wait loop. Returns process exit code (0 for success, non-zero for error/refusal).
func ExecuteAsk(ctx context.Context, cfg *config.ClientConfig, opts RunAskOptions, stdout, stderr io.Writer) int {
	if cfg == nil {
		fmt.Fprintf(stderr, "Error: client configuration is missing. Run 'talkintent pair' first.\n")
		return 1
	}
	if strings.TrimSpace(cfg.HubURL) == "" || strings.TrimSpace(cfg.Token) == "" {
		fmt.Fprintf(stderr, "Error: client node is not paired with a Hub. Run 'talkintent pair --hub <url> --code <code>' first.\n")
		return 1
	}

	target := strings.TrimSpace(opts.Target)
	queryText := strings.TrimSpace(opts.Query)

	if queryText == "" {
		fmt.Fprintf(stderr, "Error: query text is required.\n")
		fmt.Fprintf(stderr, "Usage: talkintent ask [--to <teammate>] <question>\n")
		return 1
	}

	// 1. Natural Language Target Extraction if target not specified explicitly
	if target == "" {
		members, err := FetchMembers(ctx, cfg.HubURL, cfg.Token)
		if err != nil {
			fmt.Fprintf(stderr, "Warning: failed to fetch member directory for natural-language target resolution: %v\n", err)
		} else if len(members) > 0 {
			extractedTarget, _, ambiguous, candidates := ExtractTarget(queryText, members)
			if ambiguous {
				fmt.Fprintf(stderr, "Error: question mentions multiple team members ambiguously:\n")
				for _, c := range candidates {
					aliasInfo := ""
					if c.MatchedAlias != "" {
						aliasInfo = fmt.Sprintf(" (matched %q)", c.MatchedAlias)
					}
					fmt.Fprintf(stderr, "  - %s [ID: %s]%s\n", c.Name, c.ID, aliasInfo)
				}
				fmt.Fprintf(stderr, "Please specify the exact target with --to <name>.\n")
				return 1
			}
			if extractedTarget != "" {
				target = extractedTarget
			}
		}
	}

	// 2. If target is still unresolvable, display member list (F37)
	if target == "" {
		fmt.Fprintf(stderr, "Error: unable to determine target teammate from query.\n")
		members, err := FetchMembers(ctx, cfg.HubURL, cfg.Token)
		if err == nil && len(members) > 0 {
			fmt.Fprintf(stderr, "\nAvailable Team Members:\n")
			PrintMembersTable(stderr, members)
		}
		fmt.Fprintf(stderr, "\nPlease specify target explicitly: talkintent ask --to <name> %q\n", queryText)
		return 1
	}

	// 3. Submit Query to Hub
	if opts.TimeoutSec <= 0 {
		opts.TimeoutSec = 60
	}
	if opts.TTLSec <= 0 {
		opts.TTLSec = 86400
	}

	reqBody := protocol.QuerySubmitRequest{
		Target:          target,
		Query:           queryText,
		TargetWorkspace: opts.Workspace,
		TimeoutSeconds:  opts.TimeoutSec,
		TTLSeconds:      opts.TTLSec,
		Wait:            opts.Wait,
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Fprintf(stderr, "Failed to marshal query request: %v\n", err)
		return 1
	}

	url := strings.TrimRight(cfg.HubURL, "/") + "/api/v1/queries"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBytes))
	if err != nil {
		fmt.Fprintf(stderr, "Error creating query request: %v\n", err)
		return 1
	}
	httpReq.Header.Set("Authorization", "Bearer "+cfg.Token)
	httpReq.Header.Set("Content-Type", "application/json")

	// Timeout for HTTP submit request: give extra grace period if wait is active
	httpTimeout := time.Duration(opts.TimeoutSec+15) * time.Second
	if !opts.Wait {
		httpTimeout = 15 * time.Second
	}
	client := &http.Client{Timeout: httpTimeout}

	resp, err := client.Do(httpReq)
	if err != nil {
		fmt.Fprintf(stderr, "Error submitting query: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(stderr, "Error reading response: %v\n", err)
		return 1
	}

	// Handle ambiguous target response (HTTP 300 / 400 with AMBIGUOUS_TARGET)
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusMultipleChoices {
		var targetResp protocol.TargetResolveResponse
		if json.Unmarshal(respBytes, &targetResp) == nil && targetResp.Status == "ambiguous" {
			if opts.JSONOutput {
				_, _ = stdout.Write(respBytes)
				fmt.Fprintln(stdout)
			} else {
				fmt.Fprintf(stderr, "Error: target %q matches multiple team members (Candidate Members):\n", target)
				for _, c := range targetResp.Candidates {
					aliasInfo := ""
					if c.MatchedAlias != "" {
						aliasInfo = fmt.Sprintf(" (matched alias: %s)", c.MatchedAlias)
					}
					fmt.Fprintf(stderr, "  - %s [ID: %s]%s\n", c.Name, c.ID, aliasInfo)
				}
				fmt.Fprintf(stderr, "Please specify the exact name with --to <name>.\n")
			}
			return 1
		}

		var errResp protocol.ErrorResponse
		if json.Unmarshal(respBytes, &errResp) == nil && errResp.Error.Code != "" {
			if opts.JSONOutput {
				_, _ = stdout.Write(respBytes)
				fmt.Fprintln(stdout)
			} else {
				fmt.Fprintf(stderr, "Error submitting query (%s): %s\n", errResp.Error.Code, errResp.Error.Message)
				if candidatesAny, ok := errResp.Error.Details["candidates"]; ok {
					candBytes, _ := json.Marshal(candidatesAny)
					var candList []protocol.CandidateMember
					if json.Unmarshal(candBytes, &candList) == nil && len(candList) > 0 {
						fmt.Fprintf(stderr, "Candidate Members:\n")
						for _, c := range candList {
							fmt.Fprintf(stderr, "  - %s (%s)\n", c.Name, c.ID)
						}
					}
				}
			}
			return 1
		}

		fmt.Fprintf(stderr, "Error submitting query (HTTP %d): %s\n", resp.StatusCode, string(respBytes))
		return 1
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		fmt.Fprintf(stderr, "Query rejected (HTTP %d): %s\n", resp.StatusCode, string(respBytes))
		return 1
	}

	// Check if the response is already a full QueryDetailResponse with a terminal status
	var detail protocol.QueryDetailResponse
	if err := json.Unmarshal(respBytes, &detail); err == nil && detail.QueryID != "" &&
		(detail.Status == protocol.QueryStatusCompleted ||
			detail.Status == protocol.QueryStatusSuccess ||
			detail.Status == protocol.QueryStatusRefused ||
			detail.Status == protocol.QueryStatusError) {
		return renderQueryOutcome(&detail, opts.JSONOutput, stdout, stderr)
	}

	// Otherwise it's a QuerySubmitResponse
	var submitResp protocol.QuerySubmitResponse
	if err := json.Unmarshal(respBytes, &submitResp); err != nil {
		fmt.Fprintf(stderr, "Error decoding submit response: %v\n", err)
		return 1
	}

	if !opts.Wait {
		if opts.JSONOutput {
			_ = PrintJSON(stdout, submitResp)
			return 0
		}
		fmt.Fprintf(stdout, "Query submitted [ID: %s] -> %s (Status: %s)\n",
			submitResp.QueryID, submitResp.TargetMemberName, submitResp.Status)
		return 0
	}

	// 4. Long-polling wait loop for completed probe response
	queryID := submitResp.QueryID
	targetName := submitResp.TargetMemberName
	if targetName == "" {
		targetName = target
	}

	if !opts.JSONOutput {
		fmt.Fprintf(stdout, "Query submitted [ID: %s] -> %s (Status: %s)\n", queryID, targetName, submitResp.Status)
		fmt.Fprintf(stdout, "Waiting for on-site probe response...\n")
	}

	deadline := time.Now().Add(time.Duration(opts.TimeoutSec) * time.Second)
	pollWaitSec := 15
	if pollWaitSec > opts.TimeoutSec {
		pollWaitSec = opts.TimeoutSec
	}

	for time.Now().Before(deadline) {
		pollURL := fmt.Sprintf("%s/api/v1/queries/%s?wait=%ds",
			strings.TrimRight(cfg.HubURL, "/"), queryID, pollWaitSec)

		pollReq, err := http.NewRequestWithContext(ctx, "GET", pollURL, nil)
		if err != nil {
			fmt.Fprintf(stderr, "Failed to build poll request: %v\n", err)
			return 1
		}
		pollReq.Header.Set("Authorization", "Bearer "+cfg.Token)

		pollClient := &http.Client{Timeout: time.Duration(pollWaitSec+5) * time.Second}
		pollResp, err := pollClient.Do(pollReq)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return 130
			}
			time.Sleep(1 * time.Second)
			continue
		}

		var pollDetail protocol.QueryDetailResponse
		decErr := json.NewDecoder(pollResp.Body).Decode(&pollDetail)
		pollResp.Body.Close()

		if decErr != nil {
			time.Sleep(1 * time.Second)
			continue
		}

		if pollDetail.Status != protocol.QueryStatusPending &&
			pollDetail.Status != protocol.QueryStatusDispatched &&
			pollDetail.Status != protocol.QueryStatusQueued {
			return renderQueryOutcome(&pollDetail, opts.JSONOutput, stdout, stderr)
		}
	}

	// Timed out waiting
	if opts.JSONOutput {
		_ = PrintJSON(stdout, map[string]any{
			"query_id": queryID,
			"target":   targetName,
			"status":   protocol.QueryStatusTimeout,
			"error":    "timed out waiting for probe response",
		})
	} else {
		fmt.Fprintf(stderr, "Query [ID: %s] timed out waiting for %s to respond.\n", queryID, targetName)
	}
	return 1
}

// renderQueryOutcome renders the query result and returns the appropriate exit code:
// 0 for "completed" or "success", non-zero for refused, error, timeout, expired.
func renderQueryOutcome(d *protocol.QueryDetailResponse, jsonOutput bool, stdout, stderr io.Writer) int {
	if jsonOutput {
		_ = PrintJSON(stdout, d)
		if d.Status == protocol.QueryStatusCompleted || d.Status == protocol.QueryStatusSuccess {
			return 0
		}
		return 1
	}

	switch d.Status {
	case protocol.QueryStatusCompleted, protocol.QueryStatusSuccess:
		fmt.Fprintf(stdout, "\n=== Response from %s ===\n", d.TargetMemberName)
		fmt.Fprintln(stdout, strings.TrimSpace(d.Answer))
		fmt.Fprintf(stdout, "\n[Status: %s | Tools: %s | Duration: %dms]\n",
			d.Status, strings.Join(d.ToolsUsed, ", "), d.DurationMS)
		return 0

	case protocol.QueryStatusRefused:
		fmt.Fprintf(stdout, "\n=== Response from %s ===\n", d.TargetMemberName)
		// The probe puts the (redacted) refusal reason in Answer; ErrorMessage is only
		// populated for error/timeout. Fall back to a generic line for old hubs.
		msg := d.Answer
		if msg == "" {
			msg = d.ErrorMessage
		}
		if msg == "" {
			msg = "Query refused by teammate's sovereign privacy guardrails"
		}
		fmt.Fprintf(stdout, "Status: %s\n", protocol.QueryStatusRefused)
		fmt.Fprintf(stdout, "Reason: %s\n", msg)
		return 1

	case protocol.QueryStatusTimeout:
		fmt.Fprintf(stderr, "\nQuery timed out waiting for response from %s (Status: %s)\n",
			d.TargetMemberName, d.Status)
		return 1

	case protocol.QueryStatusExpired:
		fmt.Fprintf(stderr, "\nQuery expired in offline queue before %s came online (Status: %s)\n",
			d.TargetMemberName, d.Status)
		return 1

	case protocol.QueryStatusError:
		fallthrough
	default:
		fmt.Fprintf(stderr, "\nQuery failed (Status: %s): %s\n", d.Status, d.ErrorMessage)
		return 1
	}
}
