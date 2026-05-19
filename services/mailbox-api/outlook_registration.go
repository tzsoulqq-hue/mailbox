package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	browserautomationv1 "github.com/byte-v-forge/browser-automation/gen/go/byte/v/forge/contracts/browserautomation/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	"mailboxapi/pb"
)

const (
	defaultOutlookOAuthClientID       = "9e5f94bc-e8a4-4e73-b8be-63364c29d753"
	defaultOutlookOAuthRedirectURL    = "https://login.microsoftonline.com/common/oauth2/nativeclient"
	defaultOutlookOAuthScopes         = "offline_access https://graph.microsoft.com/Mail.Read"
	defaultOutlookOAuthAuthorizeURL   = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
	defaultOutlookOAuthTokenURL       = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	defaultOutlookResultsDir          = "/app/Results"
	defaultOutlookCommandTimeout      = 180 * time.Second
	defaultOutlookBrowserSessionTTL   = 15 * time.Minute
	defaultOutlookBrowserViewportWide = 1365
	defaultOutlookBrowserViewportHigh = 768
)

type outlookRegistrationConfig struct {
	resultsDir     string
	proxyRef       string
	locale         string
	acceptLanguage string
	timezone       string
	userAgent      string
	windowWidth    int
	windowHeight   int
	sessionTTL     time.Duration
	commandTimeout time.Duration
	oauthClientID  string
	oauthRedirect  string
	oauthScopes    []string
	httpTimeout    time.Duration
}

type outlookRegistrationRunner struct {
	cfg           outlookRegistrationConfig
	browserClient browserautomationv1.BrowserAutomationServiceClient
	httpClient    *http.Client
}

type mailboxRecord struct {
	email        string
	password     string
	refreshToken string
	accessToken  string
	source       string
}

type oauthResult struct {
	refreshToken string
	accessToken  string
}

func loadOutlookRegistrationConfig() outlookRegistrationConfig {
	locale := envDefault("OUTLOOK_REGISTER_AUTOMATION_LOCALE", "en-US")
	return outlookRegistrationConfig{
		resultsDir:     envDefault("OUTLOOK_REGISTER_RESULTS_DIR", defaultOutlookResultsDir),
		proxyRef:       envDefault("OUTLOOK_REGISTER_AUTOMATION_PROXY_REF", "outlook"),
		locale:         locale,
		acceptLanguage: envDefault("OUTLOOK_REGISTER_AUTOMATION_ACCEPT_LANGUAGE", acceptLanguage(locale)),
		timezone:       envDefault("OUTLOOK_REGISTER_AUTOMATION_TIMEZONE", ""),
		userAgent:      envDefault("OUTLOOK_REGISTER_AUTOMATION_USER_AGENT", ""),
		windowWidth:    envInt("OUTLOOK_REGISTER_AUTOMATION_WINDOW_WIDTH", defaultOutlookBrowserViewportWide),
		windowHeight:   envInt("OUTLOOK_REGISTER_AUTOMATION_WINDOW_HEIGHT", defaultOutlookBrowserViewportHigh),
		sessionTTL:     envDurationSeconds("OUTLOOK_REGISTER_AUTOMATION_SESSION_TTL_SECONDS", defaultOutlookBrowserSessionTTL),
		commandTimeout: envDurationSeconds("OUTLOOK_REGISTER_AUTOMATION_COMMAND_TIMEOUT_SECONDS", defaultOutlookCommandTimeout),
		oauthClientID:  envDefault("OUTLOOK_REGISTER_OAUTH_CLIENT_ID", defaultOutlookOAuthClientID),
		oauthRedirect:  envDefault("OUTLOOK_REGISTER_OAUTH_REDIRECT_URL", defaultOutlookOAuthRedirectURL),
		oauthScopes:    splitScopes(envDefault("OUTLOOK_REGISTER_OAUTH_SCOPES", defaultOutlookOAuthScopes)),
		httpTimeout:    envDurationSeconds("OUTLOOK_REGISTER_HTTP_TIMEOUT_SECONDS", 30*time.Second),
	}
}

func newOutlookRegistrationRunner(cfg outlookRegistrationConfig, browserClient browserautomationv1.BrowserAutomationServiceClient, httpClient *http.Client) *outlookRegistrationRunner {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.httpTimeout}
	}
	return &outlookRegistrationRunner{
		cfg:           cfg,
		browserClient: browserClient,
		httpClient:    httpClient,
	}
}

func (r *outlookRegistrationRunner) RunMailboxRegistration(ctx context.Context, req *pb.RunMailboxRegistrationRequest) (*pb.RunMailboxRegistrationResponse, error) {
	records, err := readMailboxRecords(r.cfg.resultsDir, true)
	if req.GetImportOnly() {
		return registrationResponse(records, err), nil
	}
	if err != nil {
		return &pb.RunMailboxRegistrationResponse{Success: false, ExitCode: 1, ErrorMessage: err.Error()}, nil
	}
	if len(records) > 0 {
		return registrationResponse(records, nil), nil
	}
	if !req.GetEnabled() || !envBool("OUTLOOK_REGISTER_ENABLED", false) {
		return &pb.RunMailboxRegistrationResponse{Success: false, ExitCode: 0, ErrorMessage: "mailbox registration is disabled"}, nil
	}
	return &pb.RunMailboxRegistrationResponse{
		Success:      false,
		ExitCode:     1,
		ErrorMessage: "Outlook account creation is modeled in mailbox-api and must execute through browser-automation before enabling this action",
	}, nil
}

func (r *outlookRegistrationRunner) RunMailboxOAuth(ctx context.Context, req *pb.RunMailboxOAuthRequest) (*pb.RunMailboxOAuthResponse, error) {
	targets := selectOAuthTargets(req)
	if len(targets) == 0 {
		return &pb.RunMailboxOAuthResponse{
			Success:      false,
			Processed:    0,
			ErrorMessage: "mailbox OAuth accounts are required",
		}, nil
	}

	response := &pb.RunMailboxOAuthResponse{Processed: int32(len(targets))}
	for _, account := range targets {
		result := &pb.MailboxOAuthResult{EmailAddress: account.GetEmailAddress()}
		if strings.TrimSpace(account.GetPassword()) == "" {
			result.ErrorMessage = "mailbox password is required for OAuth"
			response.Failed++
			response.Results = append(response.Results, result)
			continue
		}
		tokens, err := r.runBrowserOAuth(ctx, account.GetEmailAddress(), account.GetPassword())
		if err != nil {
			result.ErrorMessage = err.Error()
			response.Failed++
			response.Results = append(response.Results, result)
			continue
		}
		result.Success = true
		result.RefreshToken = tokens.refreshToken
		result.AccessToken = tokens.accessToken
		response.Succeeded++
		response.Results = append(response.Results, result)
	}
	response.Success = response.Failed == 0 && response.Succeeded > 0
	if !response.Success {
		response.ErrorMessage = fmt.Sprintf("mailbox OAuth failed: %d/%d", response.Failed, response.Processed)
	}
	return response, nil
}

func (r *outlookRegistrationRunner) runBrowserOAuth(ctx context.Context, email string, password string) (oauthResult, error) {
	session, err := r.startSession(ctx, email)
	if err != nil {
		return oauthResult{}, err
	}
	defer r.stopSession(session)

	state := uuid.NewString()
	authURL := r.oauthAuthorizeURL(state)
	results, err := r.execute(ctx, session, "outlook.oauth", []*browserautomationv1.BrowserCommand{
		navigateCommand("open-oauth", authURL, r.cfg.commandTimeout),
		evaluateCommand("complete-oauth", outlookOAuthScript, map[string]any{
			"email":    email,
			"password": password,
		}, r.cfg.commandTimeout),
	})
	if err != nil {
		return oauthResult{}, err
	}
	resultURL := stringMapValue(commandResultMap(results, "complete-oauth"), "url")
	if resultURL == "" {
		return oauthResult{}, errors.New("OAuth browser flow did not return a redirect URL")
	}
	code, returnedState, oauthErr := oauthCodeFromURL(resultURL)
	if oauthErr != "" {
		return oauthResult{}, fmt.Errorf("OAuth redirect error: %s", oauthErr)
	}
	if returnedState != "" && returnedState != state {
		return oauthResult{}, errors.New("OAuth state mismatch")
	}
	if code == "" {
		return oauthResult{}, fmt.Errorf("OAuth code not found in redirect URL: %s", sanitizeURL(resultURL))
	}
	return r.exchangeOAuthCode(ctx, code)
}

func (r *outlookRegistrationRunner) startSession(ctx context.Context, email string) (string, error) {
	if r.browserClient == nil {
		return "", errors.New("browser-automation client is not initialized")
	}
	reqCtx, cancel := context.WithTimeout(ctx, r.cfg.commandTimeout)
	defer cancel()
	resp, err := r.browserClient.StartBrowserSession(reqCtx, &browserautomationv1.StartBrowserSessionRequest{
		RequestId: "outlook-oauth-" + uuid.NewString(),
		Profile: &browserautomationv1.BrowserProfile{
			BrowserKind: browserautomationv1.BrowserKind_BROWSER_KIND_FIREFOX,
			Locale:      r.cfg.locale,
			Timezone:    r.cfg.timezone,
			UserAgent:   r.cfg.userAgent,
			Viewport: &browserautomationv1.BrowserViewport{
				Width:  int32(r.cfg.windowWidth),
				Height: int32(r.cfg.windowHeight),
			},
			ProxyRef: r.cfg.proxyRef,
			ExtraHttpHeaders: map[string]string{
				"Accept-Language": r.cfg.acceptLanguage,
			},
			Labels: map[string]string{
				"domain":     "mailbox",
				"provider":   "outlook",
				"workflow":   "oauth",
				"email_hash": hashLabel(email),
			},
		},
		Ttl: durationpb.New(r.cfg.sessionTTL),
	})
	if err != nil {
		return "", err
	}
	if resp.GetError() != nil {
		return "", errors.New(resp.GetError().GetMessage())
	}
	sessionID := resp.GetSession().GetSessionId()
	if sessionID == "" {
		return "", errors.New("browser-automation returned empty session_id")
	}
	return sessionID, nil
}

func (r *outlookRegistrationRunner) stopSession(sessionID string) {
	if sessionID == "" || r.browserClient == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = r.browserClient.StopBrowserSession(ctx, &browserautomationv1.StopBrowserSessionRequest{
		SessionId: sessionID,
		Reason:    "outlook oauth finished",
	})
}

func (r *outlookRegistrationRunner) execute(ctx context.Context, sessionID string, taskKey string, commands []*browserautomationv1.BrowserCommand) ([]*browserautomationv1.BrowserCommandResult, error) {
	timeout := r.cfg.commandTimeout + 15*time.Second
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := r.browserClient.ExecuteBrowserCommands(reqCtx, &browserautomationv1.ExecuteBrowserCommandsRequest{
		RequestId: "outlook-oauth-" + uuid.NewString(),
		Input: &browserautomationv1.BrowserTaskInput{
			SessionId:   sessionID,
			TaskKey:     taskKey,
			ScenarioKey: "outlook.oauth",
			Timeout:     durationpb.New(timeout),
			Commands:    commands,
			Labels: map[string]string{
				"domain":   "mailbox",
				"provider": "outlook",
				"workflow": "oauth",
			},
		},
	})
	if err != nil {
		return nil, err
	}
	if resp.GetError() != nil {
		return resp.GetResults(), errors.New(resp.GetError().GetMessage())
	}
	for _, result := range resp.GetResults() {
		if result.GetStatus() == browserautomationv1.BrowserCommandStatus_BROWSER_COMMAND_STATUS_FAILED ||
			result.GetStatus() == browserautomationv1.BrowserCommandStatus_BROWSER_COMMAND_STATUS_TIMEOUT {
			if result.GetError() != nil {
				return resp.GetResults(), errors.New(result.GetError().GetMessage())
			}
			return resp.GetResults(), fmt.Errorf("browser command %s failed", result.GetCommandKey())
		}
	}
	return resp.GetResults(), nil
}

func (r *outlookRegistrationRunner) oauthAuthorizeURL(state string) string {
	values := url.Values{}
	values.Set("client_id", r.cfg.oauthClientID)
	values.Set("response_type", "code")
	values.Set("redirect_uri", r.cfg.oauthRedirect)
	values.Set("response_mode", "query")
	values.Set("scope", strings.Join(r.cfg.oauthScopes, " "))
	values.Set("state", state)
	values.Set("prompt", "login")
	return defaultOutlookOAuthAuthorizeURL + "?" + values.Encode()
}

func (r *outlookRegistrationRunner) exchangeOAuthCode(ctx context.Context, code string) (oauthResult, error) {
	values := url.Values{}
	values.Set("client_id", r.cfg.oauthClientID)
	values.Set("scope", strings.Join(r.cfg.oauthScopes, " "))
	values.Set("code", code)
	values.Set("redirect_uri", r.cfg.oauthRedirect)
	values.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, defaultOutlookOAuthTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return oauthResult{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return oauthResult{}, err
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return oauthResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oauthResult{}, fmt.Errorf("OAuth token exchange failed: %s", stringMapValue(payload, "error_description"))
	}
	refresh := stringMapValue(payload, "refresh_token")
	access := stringMapValue(payload, "access_token")
	if refresh == "" {
		return oauthResult{}, errors.New("OAuth token exchange returned empty refresh_token")
	}
	return oauthResult{refreshToken: refresh, accessToken: access}, nil
}

func registrationResponse(records []mailboxRecord, err error) *pb.RunMailboxRegistrationResponse {
	if err != nil {
		return &pb.RunMailboxRegistrationResponse{Success: false, ExitCode: 1, ErrorMessage: err.Error()}
	}
	accounts := make([]*pb.MailboxRegistrationAccount, 0, len(records))
	for _, record := range records {
		accounts = append(accounts, &pb.MailboxRegistrationAccount{
			EmailAddress: record.email,
			Password:     record.password,
			RefreshToken: record.refreshToken,
			AccessToken:  record.accessToken,
			Source:       record.source,
		})
	}
	errorMessage := ""
	if len(accounts) == 0 {
		errorMessage = "no mailbox records found to import"
	}
	return &pb.RunMailboxRegistrationResponse{
		Success:      len(accounts) > 0,
		ExitCode:     0,
		ErrorMessage: errorMessage,
		Accounts:     accounts,
	}
}

func selectOAuthTargets(req *pb.RunMailboxOAuthRequest) []*pb.MailboxRegistrationAccount {
	requested := normalizeEmail(req.GetEmailAddress())
	limit := req.GetLimit()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	targets := make([]*pb.MailboxRegistrationAccount, 0, len(req.GetAccounts()))
	for _, account := range req.GetAccounts() {
		email := normalizeEmail(account.GetEmailAddress())
		if email == "" {
			continue
		}
		if requested != "" && email != requested {
			continue
		}
		if req.GetOnlyMissing() && strings.TrimSpace(account.GetRefreshToken()) != "" {
			continue
		}
		targets = append(targets, &pb.MailboxRegistrationAccount{
			EmailAddress: email,
			Password:     strings.TrimSpace(account.GetPassword()),
			RefreshToken: strings.TrimSpace(account.GetRefreshToken()),
			AccessToken:  strings.TrimSpace(account.GetAccessToken()),
			Source:       strings.TrimSpace(account.GetSource()),
		})
		if requested == "" && int32(len(targets)) >= limit {
			break
		}
	}
	return targets
}

func readMailboxRecords(dir string, includePasswordOnly bool) ([]mailboxRecord, error) {
	recordsByEmail := map[string]mailboxRecord{}
	tokenRecords, err := parseTokenFile(filepath.Join(dir, "outlook_token.txt"))
	if err != nil {
		return nil, err
	}
	for _, record := range tokenRecords {
		recordsByEmail[record.email] = record
	}
	if includePasswordOnly {
		for _, name := range []string{"logged_email.txt", "unlogged_email.txt"} {
			passwordRecords, err := parsePasswordFile(filepath.Join(dir, name))
			if err != nil {
				return nil, err
			}
			for _, record := range passwordRecords {
				if _, exists := recordsByEmail[record.email]; !exists {
					recordsByEmail[record.email] = record
				}
			}
		}
	}
	records := make([]mailboxRecord, 0, len(recordsByEmail))
	for _, record := range recordsByEmail {
		records = append(records, record)
	}
	return records, nil
}

func parseTokenFile(path string) ([]mailboxRecord, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	records := []mailboxRecord{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "---", 5)
		if len(parts) < 3 {
			continue
		}
		email := normalizeEmail(parts[0])
		if email == "" {
			continue
		}
		record := mailboxRecord{
			email:        email,
			password:     strings.TrimSpace(parts[1]),
			refreshToken: strings.TrimSpace(parts[2]),
			source:       filepath.Base(path),
		}
		if len(parts) >= 4 {
			record.accessToken = strings.TrimSpace(parts[3])
		}
		records = append(records, record)
	}
	return records, nil
}

func parsePasswordFile(path string) ([]mailboxRecord, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	records := []mailboxRecord{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, ":") {
			continue
		}
		email, password, _ := strings.Cut(line, ":")
		if email = normalizeEmail(email); email != "" {
			records = append(records, mailboxRecord{email: email, password: strings.TrimSpace(password), source: filepath.Base(path)})
		}
	}
	return records, nil
}

func navigateCommand(commandID, targetURL string, timeout time.Duration) *browserautomationv1.BrowserCommand {
	return &browserautomationv1.BrowserCommand{
		CommandId:  commandID,
		CommandKey: commandID,
		Timeout:    durationpb.New(timeout),
		Operation: &browserautomationv1.BrowserCommand_Navigate{
			Navigate: &browserautomationv1.NavigateCommand{
				Url:       targetURL,
				WaitUntil: browserautomationv1.BrowserNavigationWaitUntil_BROWSER_NAVIGATION_WAIT_UNTIL_DOM_CONTENT_LOADED,
				Timeout:   durationpb.New(timeout),
			},
		},
	}
}

func evaluateCommand(commandID, expression string, args map[string]any, timeout time.Duration) *browserautomationv1.BrowserCommand {
	structArgs, err := structpb.NewStruct(args)
	if err != nil {
		structArgs = &structpb.Struct{}
	}
	return &browserautomationv1.BrowserCommand{
		CommandId:  commandID,
		CommandKey: commandID,
		Timeout:    durationpb.New(timeout),
		Operation: &browserautomationv1.BrowserCommand_Evaluate{
			Evaluate: &browserautomationv1.EvaluateCommand{
				Expression: expression,
				Args:       structArgs,
				Timeout:    durationpb.New(timeout),
			},
		},
	}
}

func commandResultMap(results []*browserautomationv1.BrowserCommandResult, commandID string) map[string]any {
	for _, result := range results {
		if result.GetCommandId() != commandID || result.GetJsonValue() == nil {
			continue
		}
		if value, ok := result.GetJsonValue().AsInterface().(map[string]any); ok {
			return value
		}
	}
	return nil
}

func stringMapValue(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(data[key]))
}

func oauthCodeFromURL(value string) (code string, state string, oauthErr string) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", "", err.Error()
	}
	query := parsed.Query()
	return strings.TrimSpace(query.Get("code")), strings.TrimSpace(query.Get("state")), strings.TrimSpace(query.Get("error_description"))
}

func splitScopes(value string) []string {
	parts := strings.Fields(strings.ReplaceAll(value, ",", " "))
	if len(parts) == 0 {
		return strings.Fields(defaultOutlookOAuthScopes)
	}
	return parts
}

func acceptLanguage(locale string) string {
	normalized := strings.ToLower(strings.TrimSpace(locale))
	if strings.HasPrefix(normalized, "zh") {
		return "zh-CN,zh;q=0.9,en;q=0.8"
	}
	if strings.HasPrefix(normalized, "id") {
		return "id-ID,id;q=0.9,en;q=0.8"
	}
	return "en-US,en;q=0.9"
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envBool(name string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func envDurationSeconds(name string, fallback time.Duration) time.Duration {
	return time.Duration(envInt(name, int(fallback/time.Second))) * time.Second
}

func sanitizeURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return "<invalid-url>"
	}
	query := parsed.Query()
	for _, key := range []string{"code", "state", "session_state"} {
		if query.Has(key) {
			query.Set(key, "***")
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func hashLabel(value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf("%x", uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.ToLower(value))))[:12]
}

const outlookOAuthScript = `async ({email, password}) => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const visible = (el) => {
    if (!el) return false;
    const rect = el.getBoundingClientRect();
    const style = getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
  };
  const fill = (el, value) => {
    el.focus();
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
    if (setter) setter.call(el, value); else el.value = value;
    el.dispatchEvent(new Event("input", {bubbles: true}));
    el.dispatchEvent(new Event("change", {bubbles: true}));
  };
  const clickByText = (pattern) => {
    for (const el of document.querySelectorAll("button,input[type=submit],a,div[role=button]")) {
      const text = (el.innerText || el.textContent || el.value || "").trim();
      if (!el.disabled && visible(el) && pattern.test(text)) {
        el.click();
        return true;
      }
    }
    return false;
  };
  const current = () => ({url: location.href, text: document.body ? document.body.innerText || "" : ""});
  for (let i = 0; i < 180; i++) {
    const state = current();
    if (/[?&](code|error)=/.test(state.url)) return {state: "redirected", url: state.url};
    if (/approve sign in request|help us protect|verify your identity|account.live.com\/abuse/i.test(state.text)) {
      return {state: "needs_manual_verification", url: state.url};
    }
    const emailInput = Array.from(document.querySelectorAll('input[type="email"],input[name="loginfmt"]')).find(visible);
    if (emailInput) {
      fill(emailInput, email);
      await sleep(300);
      clickByText(/^(next|continue)$/i);
      await sleep(1200);
      continue;
    }
    const passwordInput = Array.from(document.querySelectorAll('input[type="password"],input[name="passwd"]')).find(visible);
    if (passwordInput) {
      fill(passwordInput, password);
      await sleep(300);
      clickByText(/^(sign in|next|continue)$/i);
      await sleep(1500);
      continue;
    }
    if (clickByText(/^(no|yes|accept|continue|next)$/i)) {
      await sleep(1200);
      continue;
    }
    await sleep(1000);
  }
  return {state: "timeout", url: location.href};
}`
