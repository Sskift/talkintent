package web_test

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Sskift/talkintent/web"
)

// TestEmbeddedFilesNonEmpty verifies that all static assets are embedded and non-empty.
func TestEmbeddedFilesNonEmpty(t *testing.T) {
	files := []string{"static/index.html", "static/app.js", "static/style.css"}
	for _, f := range files {
		data, err := web.StaticFiles.ReadFile(f)
		if err != nil {
			t.Fatalf("failed to read embedded file %s: %v", f, err)
		}
		if len(data) == 0 {
			t.Errorf("embedded file %s is empty", f)
		}
	}

	// Test web.FS() as well
	subFS := web.FS()
	subFiles := []string{"index.html", "app.js", "style.css"}
	for _, f := range subFiles {
		data, err := fs.ReadFile(subFS, f)
		if err != nil {
			t.Fatalf("failed to read from web.FS() %s: %v", f, err)
		}
		if len(data) == 0 {
			t.Errorf("file %s in web.FS() is empty", f)
		}
	}
}

// TestMIMETypes verifies that static assets are served with the correct MIME types.
func TestMIMETypes(t *testing.T) {
	handler := web.Handler()

	tests := []struct {
		name             string
		path             string
		expectedStatus   int
		expectedMIMEPart string
	}{
		{"root path", "/", http.StatusOK, "text/html"},
		{"web prefix root", "/web", http.StatusOK, "text/html"},
		{"web prefix slash", "/web/", http.StatusOK, "text/html"},
		{"index html", "/index.html", http.StatusOK, "text/html"},
		{"web index html", "/web/index.html", http.StatusOK, "text/html"},
		{"css file", "/style.css", http.StatusOK, "text/css"},
		{"web css file", "/web/style.css", http.StatusOK, "text/css"},
		{"js file", "/app.js", http.StatusOK, "javascript"},
		{"web js file", "/web/app.js", http.StatusOK, "javascript"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.expectedStatus {
				t.Errorf("path %s: expected status %d, got %d", tc.path, tc.expectedStatus, rec.Code)
			}

			contentType := rec.Header().Get("Content-Type")
			if !strings.Contains(contentType, tc.expectedMIMEPart) {
				t.Errorf("path %s: expected Content-Type containing %q, got %q", tc.path, tc.expectedMIMEPart, contentType)
			}

			body := rec.Body.Bytes()
			if len(body) == 0 {
				t.Errorf("path %s returned empty body", tc.path)
			}
		})
	}
}

// TestFileServerWithWebFS tests stdlib http.FileServer compatibility when mounted with web.FS().
func TestFileServerWithWebFS(t *testing.T) {
	server := httptest.NewServer(http.FileServer(http.FS(web.FS())))
	defer server.Close()

	client := server.Client()

	resp, err := client.Get(server.URL + "/index.html")
	if err != nil {
		t.Fatalf("failed to GET /index.html: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("expected Content-Type containing text/html, got %s", ct)
	}

	// Test CSS
	respCSS, err := client.Get(server.URL + "/style.css")
	if err != nil {
		t.Fatalf("failed to GET /style.css: %v", err)
	}
	defer respCSS.Body.Close()
	if ct := respCSS.Header.Get("Content-Type"); !strings.Contains(ct, "text/css") {
		t.Errorf("expected Content-Type containing text/css, got %s", ct)
	}

	// Test JS
	respJS, err := client.Get(server.URL + "/app.js")
	if err != nil {
		t.Fatalf("failed to GET /app.js: %v", err)
	}
	defer respJS.Body.Close()
	if ct := respJS.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("expected Content-Type containing javascript, got %s", ct)
	}
}

// TestRequiredDOMIDsPresent verifies that all required DOM elements exist in index.html.
func TestRequiredDOMIDsPresent(t *testing.T) {
	htmlData, err := fs.ReadFile(web.FS(), "index.html")
	if err != nil {
		t.Fatalf("failed to read index.html: %v", err)
	}
	htmlStr := string(htmlData)

	requiredIDs := []string{
		// Auth & User Header
		"auth-status",
		"user-badge",
		"user-info-text",
		"btn-open-login",
		"btn-logout",

		// Navigation Buttons
		"nav-btn-members",
		"nav-btn-inbound",
		"nav-btn-outbound",
		"nav-btn-ask",
		"nav-btn-detail",
		"nav-btn-feishu",
		"nav-btn-admin",

		// Tabs
		"tab-members",
		"tab-inbound",
		"tab-outbound",
		"tab-ask",
		"tab-detail",
		"tab-feishu",
		"tab-admin",

		// Members View
		"members-table-body",
		"btn-refresh-members",
		"members-count",
		"members-datalist",

		// Inbound View
		"inbound-table-body",
		"btn-refresh-inbound",
		"inbound-count",

		// Outbound View
		"outbound-table-body",
		"btn-refresh-outbound",
		"outbound-count",

		// Ask Form View
		"ask-target",
		"ask-query",
		"ask-workspace",
		"ask-timeout",
		"ask-submit-btn",
		"ask-cancel-btn",
		"ask-progress",
		"ask-progress-text",
		"ask-candidates",
		"ask-result",
		"ask-status-badge",
		"ask-duration",
		"ask-tokens",
		"ask-tools",
		"ask-answer",
		"ask-copy-btn",
		"ask-detail-btn",

		// Query Detail Timeline View
		"detail-query-id",
		"detail-fetch-btn",
		"detail-container",
		"detail-status",
		"detail-duration",
		"detail-origin",
		"detail-workspace",
		"detail-asker",
		"detail-target",
		"detail-query-text",
		"detail-tokens",
		"detail-timeline",
		"detail-tools",
		"detail-error",
		"detail-answer",
		"detail-copy-btn",

		// Feishu Binding View
		"btn-refresh-feishu",
		"fs-bound-card",
		"fs-status-badge",
		"fs-connected-at",
		"fs-reconnects",
		"fs-delete-btn",
		"fs-app-id",
		"fs-app-secret",
		"fs-save-btn",
		"fs-result",

		// Admin Invite View
		"admin-token",
		"inv-name",
		"inv-aliases",
		"inv-expires",
		"inv-submit-btn",
		"invite-result",
		"invite-code-display",
		"invite-command-display",
		"invite-copy-btn",
		"invite-copy-cmd-btn",

		// Login Modal
		"login-modal",
		"token-input",
		"btn-login-submit",
		"btn-login-cancel",
		"login-msg",

		// Global Toasts
		"toast-container",
	}

	for _, id := range requiredIDs {
		// Matches id="id" or id='id'
		re := regexp.MustCompile(`\bid=["']` + regexp.QuoteMeta(id) + `["']`)
		if !re.MatchString(htmlStr) {
			t.Errorf("missing required DOM element ID: %q in index.html", id)
		}
	}
}

// TestAllHTMLHandlersDefinedInJS verifies that every event handler referenced in HTML
// (e.g. onclick="funcName(...)") is actually defined in app.js.
func TestAllHTMLHandlersDefinedInJS(t *testing.T) {
	htmlData, err := fs.ReadFile(web.FS(), "index.html")
	if err != nil {
		t.Fatalf("failed to read index.html: %v", err)
	}
	jsData, err := fs.ReadFile(web.FS(), "app.js")
	if err != nil {
		t.Fatalf("failed to read app.js: %v", err)
	}

	htmlStr := string(htmlData)
	jsStr := string(jsData)

	// Regex to extract handler expressions from on* attributes:
	// onclick="foo(...)" or onclick='foo(...)'
	handlerAttrRe := regexp.MustCompile(`\bon[a-z]+\s*=\s*["']([^"']+)["']`)
	matches := handlerAttrRe.FindAllStringSubmatch(htmlStr, -1)
	if len(matches) == 0 {
		t.Fatalf("no event handlers found in index.html, expected multiple onclick handlers")
	}

	// Function call regex: extract the leading function name e.g. showTab from showTab('members')
	funcNameRe := regexp.MustCompile(`^([a-zA-Z0-9_$]+)\s*\(`)

	checkedFuncs := make(map[string]bool)
	for _, m := range matches {
		rawCall := strings.TrimSpace(m[1])
		fnMatch := funcNameRe.FindStringSubmatch(rawCall)
		if len(fnMatch) < 2 {
			continue
		}
		fnName := fnMatch[1]
		if checkedFuncs[fnName] {
			continue
		}
		checkedFuncs[fnName] = true

		// Check if defined in JS:
		// 1) function fnName
		// 2) window.fnName =
		// 3) const fnName = / let fnName = / var fnName =
		fnDef1 := regexp.MustCompile(`function\s+` + regexp.QuoteMeta(fnName) + `\b`)
		fnDef2 := regexp.MustCompile(`\bwindow\.` + regexp.QuoteMeta(fnName) + `\s*=`)
		fnDef3 := regexp.MustCompile(`\b(const|let|var)\s+` + regexp.QuoteMeta(fnName) + `\s*=`)

		if !fnDef1.MatchString(jsStr) && !fnDef2.MatchString(jsStr) && !fnDef3.MatchString(jsStr) {
			t.Errorf("HTML references handler %q, but %s is not defined in app.js", rawCall, fnName)
		}
	}

	if len(checkedFuncs) == 0 {
		t.Errorf("no valid handler function calls were extracted from HTML")
	}
}

// TestStatusVocabularyInCSSAndJS verifies that all protocol status constants
// are covered in style.css and app.js.
func TestStatusVocabularyInCSSAndJS(t *testing.T) {
	cssData, err := fs.ReadFile(web.FS(), "style.css")
	if err != nil {
		t.Fatalf("failed to read style.css: %v", err)
	}
	jsData, err := fs.ReadFile(web.FS(), "app.js")
	if err != nil {
		t.Fatalf("failed to read app.js: %v", err)
	}

	cssStr := string(cssData)
	jsStr := string(jsData)

	// Statuses from PROTOCOL.md & rest.go line 100:
	// "dispatched", "queued", "completed", "refused", "error", "timeout", "expired"
	statuses := []string{
		"dispatched",
		"queued",
		"completed",
		"refused",
		"error",
		"timeout",
		"expired",
	}

	for _, s := range statuses {
		badgeClass := ".badge-" + s
		if !strings.Contains(cssStr, badgeClass) {
			t.Errorf("style.css is missing status badge class %q", badgeClass)
		}
		if !strings.Contains(jsStr, s) {
			t.Errorf("app.js does not handle status %q", s)
		}
	}
}

// TestSecurityTokenNeverInURL verifies that client JS stores token in localStorage,
// strips token from URL hash, and sends Authorization header.
func TestSecurityTokenNeverInURL(t *testing.T) {
	jsData, err := fs.ReadFile(web.FS(), "app.js")
	if err != nil {
		t.Fatalf("failed to read app.js: %v", err)
	}
	jsStr := string(jsData)

	// Must check location.hash for token parameter
	if !strings.Contains(jsStr, "window.location.hash") {
		t.Errorf("app.js must inspect window.location.hash for token")
	}

	// Must clear hash with history.replaceState
	if !strings.Contains(jsStr, "history.replaceState") {
		t.Errorf("app.js must clear hash from browser history using history.replaceState")
	}

	// Must use localStorage for token storage
	if !strings.Contains(jsStr, "localStorage.getItem('talkintent_token')") {
		t.Errorf("app.js must read token from localStorage")
	}

	// Must attach Authorization header
	if !strings.Contains(jsStr, "Authorization': 'Bearer ' + token") && !strings.Contains(jsStr, "Authorization") {
		t.Errorf("app.js must attach Authorization: Bearer token header")
	}
}

// TestHandlerSPAFallback verifies that subroutes under /web return index.html for SPA routing.
func TestHandlerSPAFallback(t *testing.T) {
	handler := web.Handler()

	req := httptest.NewRequest("GET", "/web/unknown-subroute", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Status should be 200 OK serving index.html
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for SPA fallback, got %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "TalkIntent 研发协同感知控制台") {
		t.Errorf("expected index.html content in SPA fallback, got %s", string(body)[:min(100, len(body))])
	}
}

// TestImmediateQueryTerminalHandling verifies that app.js renders immediate terminal query
// responses (completed/refused/error/timeout/expired) without redundant long-polling roundtrips.
func TestImmediateQueryTerminalHandling(t *testing.T) {
	jsData, err := fs.ReadFile(web.FS(), "app.js")
	if err != nil {
		t.Fatalf("failed to read app.js: %v", err)
	}
	jsStr := string(jsData)

	// Verify terminal status list is defined
	if !strings.Contains(jsStr, "TERMINAL_STATUSES") && !strings.Contains(jsStr, "terminalStatuses") {
		t.Errorf("app.js must define terminal status array for query state checks")
	}

	// Verify submitAskQuery inspects data.status against terminal statuses
	if !strings.Contains(jsStr, "TERMINAL_STATUSES.includes(data.status)") &&
		!strings.Contains(jsStr, "terminalStatuses.includes(data.status)") {
		t.Errorf("submitAskQuery must check if data.status is terminal before starting polling")
	}

	// Verify renderAskResult is called on immediate terminal response
	if !strings.Contains(jsStr, "renderAskResult(data)") {
		t.Errorf("submitAskQuery must call renderAskResult on immediate terminal response")
	}
}

// TestInvitePairCommandFormat verifies that generateInvite in app.js formats the onboarding
// pairing command with both --hub and --code flags so it works out of the box in the CLI.
func TestInvitePairCommandFormat(t *testing.T) {
	jsData, err := fs.ReadFile(web.FS(), "app.js")
	if err != nil {
		t.Fatalf("failed to read app.js: %v", err)
	}
	jsStr := string(jsData)

	// Must include --hub and --code flags with origin and code
	expectedPattern := `talkintent pair --hub ${window.location.origin} --code ${data.code}`
	if !strings.Contains(jsStr, expectedPattern) {
		t.Errorf("app.js generateInvite must format pair command as %q", expectedPattern)
	}
}
