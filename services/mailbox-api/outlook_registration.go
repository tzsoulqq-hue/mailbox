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
	outlookProxy  *outlookProxyManager
}

type mailboxRecord struct {
	email        string
	password     string
	refreshToken string
	accessToken  string
	source       string
	homeCountry  string
	homeIP       string
	proxyProfile string
}

type oauthResult struct {
	refreshToken string
	accessToken  string
}

type outlookRegistrationProgress func(step string)

func loadOutlookRegistrationConfig() outlookRegistrationConfig {
	locale := envDefault("OUTLOOK_REGISTER_AUTOMATION_LOCALE", "en-US")
	return outlookRegistrationConfig{
		resultsDir:     envDefault("OUTLOOK_REGISTER_RESULTS_DIR", defaultOutlookResultsDir),
		proxyRef:       envDefault("OUTLOOK_REGISTER_AUTOMATION_PROXY_REF", "outlook"),
		locale:         locale,
		acceptLanguage: envDefault("OUTLOOK_REGISTER_AUTOMATION_ACCEPT_LANGUAGE", acceptLanguage(locale)),
		timezone:       envDefault("OUTLOOK_REGISTER_AUTOMATION_TIMEZONE", ""),
		userAgent:      envDefault("OUTLOOK_REGISTER_AUTOMATION_USER_AGENT", ""),
		windowWidth:    envPositiveInt("OUTLOOK_REGISTER_AUTOMATION_WINDOW_WIDTH", defaultOutlookBrowserViewportWide),
		windowHeight:   envPositiveInt("OUTLOOK_REGISTER_AUTOMATION_WINDOW_HEIGHT", defaultOutlookBrowserViewportHigh),
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
		outlookProxy:  newOutlookProxyManagerFromEnv(),
	}
}

func (r *outlookRegistrationRunner) RunMailboxRegistration(ctx context.Context, req *pb.RunMailboxRegistrationRequest) (*pb.RunMailboxRegistrationResponse, error) {
	return r.RunMailboxRegistrationWithProgress(ctx, req, nil)
}

func (r *outlookRegistrationRunner) RunMailboxRegistrationWithProgress(ctx context.Context, req *pb.RunMailboxRegistrationRequest, progress outlookRegistrationProgress) (*pb.RunMailboxRegistrationResponse, error) {
	progressStep(progress, "registration_check_results")
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

	count := int(req.GetMaxCount())
	if count <= 0 {
		count = envPositiveInt("OUTLOOK_REGISTER_MAX_TASKS", 1)
	}
	if count > 5 {
		count = 5
	}
	response := &pb.RunMailboxRegistrationResponse{}
	for i := 0; i < count; i++ {
		progressStep(progress, fmt.Sprintf("registration_account_%d_prepare", i+1))
		record, err := r.registerOutlookAccount(ctx, progress)
		if err != nil {
			response.ErrorMessage = appendRegistrationError(response.GetErrorMessage(), err.Error())
			continue
		}
		response.Accounts = append(response.Accounts, &pb.MailboxRegistrationAccount{
			EmailAddress:           record.email,
			Password:               record.password,
			RefreshToken:           record.refreshToken,
			AccessToken:            record.accessToken,
			Source:                 record.source,
			HomeCountry:            record.homeCountry,
			HomeIp:                 record.homeIP,
			ProxyProfile:           record.proxyProfile,
			ManualRecoveryRequired: strings.TrimSpace(record.refreshToken) == "",
		})
	}
	response.Success = len(response.GetAccounts()) > 0
	if !response.Success {
		response.ExitCode = 1
		if response.GetErrorMessage() == "" {
			response.ErrorMessage = "Outlook registration did not create any account"
		}
	}
	return response, nil
}

func progressStep(progress outlookRegistrationProgress, step string) {
	if progress != nil {
		progress(step)
	}
}

func (r *outlookRegistrationRunner) registerOutlookAccount(ctx context.Context, progress outlookRegistrationProgress) (mailboxRecord, error) {
	emailLocal := generateOutlookEmailLocal()
	emailSuffix := envDefault("OUTLOOK_REGISTER_EMAIL_SUFFIX", "@outlook.com")
	if !strings.HasPrefix(emailSuffix, "@") {
		emailSuffix = "@" + emailSuffix
	}
	email := normalizeEmail(emailLocal + strings.ToLower(emailSuffix))
	password := generateOutlookPassword()
	homeCountry := strings.ToUpper(strings.TrimSpace(envDefault("OUTLOOK_REGISTER_HOME_COUNTRY", "")))
	if homeCountry == "" && r.outlookProxy != nil {
		homeCountry = strings.ToUpper(strings.TrimSpace(r.outlookProxy.region))
	}

	progressStep(progress, "registration_start_browser")
	session, proxySession, err := r.startSession(ctx, email, homeCountry, "register", true)
	if err != nil {
		return mailboxRecord{}, err
	}
	defer r.stopSession(session)

	progressStep(progress, "registration_open_outlook_signup")
	if _, err := r.execute(ctx, session, "outlook.register.open", []*browserautomationv1.BrowserCommand{
		navigateCommandWithWait("open-signup", "https://outlook.live.com/mail/0/?prompt=create_account", 45*time.Second, browserautomationv1.BrowserNavigationWaitUntil_BROWSER_NAVIGATION_WAIT_UNTIL_COMMIT),
	}); err != nil {
		return mailboxRecord{}, err
	}
	progressStep(progress, "registration_signup_page_probe")
	if _, err := r.execute(ctx, session, "outlook.register.probe", []*browserautomationv1.BrowserCommand{
		pageStateCommand("signup-page-state", true, 10*time.Second),
	}); err != nil {
		return mailboxRecord{}, err
	}
	progressStep(progress, "registration_submit_outlook_signup")
	result, err := r.runOutlookRegisterStepLoop(ctx, session, map[string]any{
		"email":      email,
		"emailLocal": emailLocal,
		"password":   password,
		"firstName":  generateOutlookFirstName(),
		"lastName":   generateOutlookLastName(),
		"birthYear":  fmt.Sprintf("%d", 1970+envPositiveInt("OUTLOOK_REGISTER_BIRTH_YEAR_OFFSET", 22)%30),
		"birthMonth": fmt.Sprintf("%d", (envPositiveInt("OUTLOOK_REGISTER_BIRTH_MONTH", 1)-1)%12+1),
		"birthDay":   fmt.Sprintf("%d", (envPositiveInt("OUTLOOK_REGISTER_BIRTH_DAY", 12)-1)%28+1),
	})
	if err != nil {
		return mailboxRecord{}, err
	}
	state := stringMapValue(result, "state")
	if state != "registered" && state != "mailbox_opened" {
		detail := stringMapValue(result, "message")
		if detail == "" {
			detail = stringMapValue(result, "url")
		}
		if detail == "" {
			detail = "unknown registration state"
		}
		return mailboxRecord{}, fmt.Errorf("Outlook registration failed: %s %s", state, detail)
	}

	record := mailboxRecord{
		email:        email,
		password:     password,
		source:       "browser_automation",
		homeCountry:  homeCountry,
		proxyProfile: proxySession,
	}
	if envBool("OUTLOOK_REGISTER_ENABLE_OAUTH2", true) {
		progressStep(progress, "registration_oauth")
		tokens, err := r.runBrowserOAuth(ctx, email, password, homeCountry, true)
		if err != nil {
			record.source = "browser_automation_oauth_pending"
			return record, nil
		}
		record.refreshToken = tokens.refreshToken
		record.accessToken = tokens.accessToken
	}
	progressStep(progress, "registration_account_ready")
	return record, nil
}

func (r *outlookRegistrationRunner) runOutlookRegisterStepLoop(ctx context.Context, session string, args map[string]any) (map[string]any, error) {
	attempts := envPositiveInt("OUTLOOK_REGISTER_STEP_ATTEMPTS", 150)
	if attempts > 300 {
		attempts = 300
	}
	var last map[string]any
	for i := 0; i < attempts; i++ {
		results, err := r.execute(ctx, session, "outlook.register.submit", []*browserautomationv1.BrowserCommand{
			evaluateCommand("create-outlook-account", outlookRegisterScript, args, 8*time.Second),
		})
		if err != nil {
			return last, err
		}
		result := commandResultMap(results, "create-outlook-account")
		if result != nil {
			last = result
		}
		state := stringMapValue(result, "state")
		switch state {
		case "registered", "mailbox_opened", "needs_manual_verification", "rate_limited":
			return result, nil
		case "captcha_required":
			if err := r.solveOutlookCaptcha(ctx, session); err != nil {
				return result, err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	if last == nil {
		last = map[string]any{"state": "timeout", "message": "Outlook registration did not return page state"}
	} else {
		last["state"] = "timeout"
	}
	return last, nil
}

func (r *outlookRegistrationRunner) solveOutlookCaptcha(ctx context.Context, session string) error {
	attempts := envPositiveInt("OUTLOOK_REGISTER_CAPTCHA_ATTEMPTS", 3)
	if attempts > 5 {
		attempts = 5
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		results, err := r.execute(ctx, session, "outlook.register.captcha", []*browserautomationv1.BrowserCommand{
			clickCommand("press-and-hold-captcha", []*browserautomationv1.BrowserSelector{
				cssSelector("[aria-label*='Press and hold' i]"),
				cssSelector("[aria-label*='Press' i]"),
				cssSelector("button:has-text('Press and hold')"),
				cssSelector("button:has-text('Press')"),
				textSelector("Press and hold"),
				textSelector("Press again"),
			}, 5*time.Second, 6500*time.Millisecond),
			evaluateCommand("captcha-after-wait", `async () => {
				await new Promise((resolve) => setTimeout(resolve, 8000));
				const text = document.body ? document.body.innerText || "" : "";
				return {url: location.href, text: text.slice(0, 500)};
			}`, nil, 12*time.Second),
		})
		if err != nil {
			lastErr = err
		} else {
			state := commandResultMap(results, "captcha-after-wait")
			text := stringMapValue(state, "text")
			if !strings.Contains(strings.ToLower(text), "press and hold") && !strings.Contains(strings.ToLower(text), "prove you're human") {
				return nil
			}
			lastErr = errors.New("captcha press-and-hold did not clear challenge")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("captcha press-and-hold did not complete")
	}
	return lastErr
}

func appendRegistrationError(existing string, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return next
	}
	return existing + "; " + next
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
		tokens, err := r.runBrowserOAuth(ctx, account.GetEmailAddress(), account.GetPassword(), account.GetHomeCountry(), account.GetManualRecoveryRequired())
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

func (r *outlookRegistrationRunner) runBrowserOAuth(ctx context.Context, email string, password string, homeCountry string, reuseCurrentProxy bool) (oauthResult, error) {
	session, _, err := r.startSession(ctx, email, homeCountry, "oauth", !reuseCurrentProxy)
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
	return r.exchangeOAuthCode(ctx, code, r.httpClientForCurrentProxy())
}

type outlookManualRecoverySession struct {
	email         string
	sessionID     string
	proxyCountry  string
	proxySession  string
	localProxyURL string
	recoveryURL   string
	launchCommand string
	instruction   string
}

func (r *outlookRegistrationRunner) StartManualRecovery(ctx context.Context, mailbox *pb.EmailMailbox) (outlookManualRecoverySession, error) {
	if mailbox == nil || normalizeEmail(mailbox.GetEmailAddress()) == "" {
		return outlookManualRecoverySession{}, errors.New("mailbox email_address is required")
	}
	sessionID, proxySession, err := r.startSession(ctx, mailbox.GetEmailAddress(), mailbox.GetHomeCountry(), "manual_recovery", true)
	if err != nil {
		return outlookManualRecoverySession{}, err
	}
	_, err = r.execute(ctx, sessionID, "outlook.manual_recovery", []*browserautomationv1.BrowserCommand{
		navigateCommand("open-login", "https://login.live.com/", r.cfg.commandTimeout),
	})
	if err != nil {
		return outlookManualRecoverySession{}, err
	}
	return outlookManualRecoverySession{
		email:        normalizeEmail(mailbox.GetEmailAddress()),
		sessionID:    sessionID,
		proxyCountry: strings.ToUpper(strings.TrimSpace(mailbox.GetHomeCountry())),
		proxySession: proxySession,
	}, nil
}

func (r *outlookRegistrationRunner) PrepareLocalManualRecovery(ctx context.Context, mailbox *pb.EmailMailbox) (outlookManualRecoverySession, error) {
	if mailbox == nil || normalizeEmail(mailbox.GetEmailAddress()) == "" {
		return outlookManualRecoverySession{}, errors.New("mailbox email_address is required")
	}
	if r.outlookProxy == nil || !r.outlookProxy.enabled() {
		return outlookManualRecoverySession{}, errors.New("Outlook proxy is not configured")
	}
	email := normalizeEmail(mailbox.GetEmailAddress())
	country := strings.ToUpper(strings.TrimSpace(mailbox.GetHomeCountry()))
	proxySession, err := r.outlookProxy.rotateSession(ctx, email, country)
	if err != nil {
		return outlookManualRecoverySession{}, err
	}
	localProxyURL := localManualRecoveryProxyURL()
	recoveryURL := "https://login.live.com/"
	command := localManualRecoveryLaunchCommand(email, localProxyURL, recoveryURL)
	return outlookManualRecoverySession{
		email:         email,
		sessionID:     "local-visible-browser",
		proxyCountry:  country,
		proxySession:  proxySession,
		localProxyURL: localProxyURL,
		recoveryURL:   recoveryURL,
		launchCommand: command,
		instruction:   "Run the launch command on this Windows client and complete Microsoft verification in the dedicated browser. Do not refresh the Sticky session or change the outbound IP during recovery.",
	}, nil
}

func (r *outlookRegistrationRunner) startSession(ctx context.Context, email string, homeCountry string, workflow string, rotateProxy bool) (string, string, error) {
	if r.browserClient == nil {
		return "", "", errors.New("browser-automation client is not initialized")
	}
	proxySession := ""
	if rotateProxy && r.outlookProxy != nil && r.outlookProxy.enabled() {
		hash, err := r.outlookProxy.rotateSession(ctx, email, homeCountry)
		if err != nil {
			return "", "", err
		}
		proxySession = hash
	}
	reqCtx, cancel := context.WithTimeout(ctx, r.cfg.commandTimeout)
	defer cancel()
	if workflow == "" {
		workflow = "oauth"
	}
	labels := map[string]string{
		"domain":        "mailbox",
		"provider":      "outlook",
		"workflow":      workflow,
		"email_hash":    hashLabel(email),
		"home_country":  strings.ToUpper(strings.TrimSpace(homeCountry)),
		"proxy_session": proxySession,
	}
	if workflow == "register" && envBool("OUTLOOK_REGISTER_BLOCK_BROWSER_RESOURCES", true) {
		labels["camoufox.block_resources"] = "true"
		labels["camoufox.block_resource_types"] = envDefault("OUTLOOK_REGISTER_BLOCK_RESOURCE_TYPES", "image,font,media")
		labels["camoufox.block_url_patterns"] = envDefault("OUTLOOK_REGISTER_BLOCK_URL_PATTERNS", "gvt1.com,edgedl.me.gvt1.com,google-analytics.com,googletagmanager.com,clarity.ms,bat.bing.com,events.data.microsoft.com,arc.msn.com,collector.azure.com")
	}
	resp, err := r.browserClient.StartBrowserSession(reqCtx, &browserautomationv1.StartBrowserSessionRequest{
		RequestId: "outlook-" + workflow + "-" + uuid.NewString(),
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
			Labels: labels,
		},
		Ttl: durationpb.New(r.cfg.sessionTTL),
	})
	if err != nil {
		return "", "", err
	}
	if resp.GetError() != nil {
		return "", "", errors.New(resp.GetError().GetMessage())
	}
	sessionID := resp.GetSession().GetSessionId()
	if sessionID == "" {
		return "", "", errors.New("browser-automation returned empty session_id")
	}
	return sessionID, proxySession, nil
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
	timeout := browserCommandsTimeout(commands, r.cfg.commandTimeout) + 15*time.Second
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

func browserCommandsTimeout(commands []*browserautomationv1.BrowserCommand, fallback time.Duration) time.Duration {
	maxTimeout := time.Duration(0)
	for _, command := range commands {
		if command == nil || command.GetTimeout() == nil {
			continue
		}
		if value := command.GetTimeout().AsDuration(); value > maxTimeout {
			maxTimeout = value
		}
	}
	if maxTimeout <= 0 {
		maxTimeout = fallback
	}
	return maxTimeout
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

func (r *outlookRegistrationRunner) httpClientForCurrentProxy() *http.Client {
	if r.outlookProxy == nil || !r.outlookProxy.enabled() || strings.TrimSpace(r.outlookProxy.proxyURL) == "" {
		return r.httpClient
	}
	client, err := httpClientForProxyURL(r.outlookProxy.proxyURL, r.outlookProxy.timeout)
	if err != nil {
		return r.httpClient
	}
	return client
}

func (r *outlookRegistrationRunner) exchangeOAuthCode(ctx context.Context, code string, httpClient *http.Client) (oauthResult, error) {
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
	if httpClient == nil {
		httpClient = r.httpClient
	}
	resp, err := httpClient.Do(req)
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
			HomeCountry:  record.homeCountry,
			HomeIp:       record.homeIP,
			ProxyProfile: record.proxyProfile,
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
			EmailAddress:           email,
			Password:               strings.TrimSpace(account.GetPassword()),
			RefreshToken:           strings.TrimSpace(account.GetRefreshToken()),
			AccessToken:            strings.TrimSpace(account.GetAccessToken()),
			Source:                 strings.TrimSpace(account.GetSource()),
			HomeCountry:            strings.ToUpper(strings.TrimSpace(account.GetHomeCountry())),
			HomeIp:                 strings.TrimSpace(account.GetHomeIp()),
			ProxyProfile:           strings.TrimSpace(account.GetProxyProfile()),
			ManualRecoveryRequired: account.GetManualRecoveryRequired(),
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
	return navigateCommandWithWait(commandID, targetURL, timeout, browserautomationv1.BrowserNavigationWaitUntil_BROWSER_NAVIGATION_WAIT_UNTIL_DOM_CONTENT_LOADED)
}

func navigateCommandWithWait(commandID, targetURL string, timeout time.Duration, waitUntil browserautomationv1.BrowserNavigationWaitUntil) *browserautomationv1.BrowserCommand {
	return &browserautomationv1.BrowserCommand{
		CommandId:  commandID,
		CommandKey: commandID,
		Timeout:    durationpb.New(timeout),
		Operation: &browserautomationv1.BrowserCommand_Navigate{
			Navigate: &browserautomationv1.NavigateCommand{
				Url:       targetURL,
				WaitUntil: waitUntil,
				Timeout:   durationpb.New(timeout),
			},
		},
	}
}

func pageStateCommand(commandID string, includeText bool, timeout time.Duration) *browserautomationv1.BrowserCommand {
	return &browserautomationv1.BrowserCommand{
		CommandId:  commandID,
		CommandKey: commandID,
		Timeout:    durationpb.New(timeout),
		Operation: &browserautomationv1.BrowserCommand_GetPageState{
			GetPageState: &browserautomationv1.GetPageStateCommand{
				IncludeTitle: true,
				IncludeText:  includeText,
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

func cssSelector(value string) *browserautomationv1.BrowserSelector {
	return &browserautomationv1.BrowserSelector{
		Kind:  browserautomationv1.BrowserSelectorKind_BROWSER_SELECTOR_KIND_CSS,
		Value: value,
	}
}

func textSelector(value string) *browserautomationv1.BrowserSelector {
	return &browserautomationv1.BrowserSelector{
		Kind:  browserautomationv1.BrowserSelectorKind_BROWSER_SELECTOR_KIND_TEXT,
		Value: value,
	}
}

func clickCommand(commandID string, selectors []*browserautomationv1.BrowserSelector, timeout time.Duration, hold time.Duration) *browserautomationv1.BrowserCommand {
	return &browserautomationv1.BrowserCommand{
		CommandId:  commandID,
		CommandKey: commandID,
		Timeout:    durationpb.New(timeout + hold + 2*time.Second),
		Operation: &browserautomationv1.BrowserCommand_Click{
			Click: &browserautomationv1.ClickCommand{
				SelectorGroup: &browserautomationv1.BrowserSelectorGroup{
					Selectors: selectors,
					Timeout:   durationpb.New(timeout),
				},
				Force:        true,
				Button:       browserautomationv1.BrowserMouseButton_BROWSER_MOUSE_BUTTON_LEFT,
				HoldDuration: durationpb.New(hold),
				Timeout:      durationpb.New(timeout),
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

func generateOutlookEmailLocal() string {
	first := strings.ToLower(generateOutlookFirstName())
	last := strings.ToLower(generateOutlookLastName())
	suffix, err := randomHex(3)
	if err != nil {
		suffix = shortValueHash(fmt.Sprintf("%d", time.Now().UnixNano()))[:6]
	}
	local := first + last + suffix
	if len(local) > 24 {
		local = local[:24]
	}
	return local
}

func generateOutlookPassword() string {
	raw, err := randomHex(6)
	if err != nil {
		raw = shortValueHash(fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	return "M." + raw[:6] + "_" + raw[6:] + "!Q7"
}

func generateOutlookFirstName() string {
	names := []string{"James", "Robert", "John", "Michael", "David", "William", "Richard", "Thomas", "Daniel", "Andrew", "Jessica", "Ashley", "Emily", "Sarah", "Amanda", "Nicole"}
	return names[time.Now().Nanosecond()%len(names)]
}

func generateOutlookLastName() string {
	names := []string{"Smith", "Johnson", "Brown", "Taylor", "Anderson", "Thomas", "Moore", "Martin", "Jackson", "White", "Harris", "Clark", "Lewis", "Walker", "Young", "King"}
	return names[(time.Now().Nanosecond()/1000)%len(names)]
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

func envPositiveInt(name string, fallback int) int {
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
	return time.Duration(envPositiveInt(name, int(fallback/time.Second))) * time.Second
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

const outlookRegisterScript = `async ({email, emailLocal, password, firstName, lastName, birthYear, birthMonth, birthDay}) => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const visible = (el) => {
    if (!el) return false;
    const rect = el.getBoundingClientRect();
    const style = getComputedStyle(el);
    return rect.width > 0 && rect.height > 0 && style.visibility !== "hidden" && style.display !== "none";
  };
  const textOf = (el) => (el?.innerText || el?.textContent || el?.value || "").trim();
  const fill = (el, value) => {
    el.focus();
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set || Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")?.set;
    if (setter) setter.call(el, value); else el.value = value;
    el.dispatchEvent(new InputEvent("input", {bubbles: true, inputType: "insertText", data: value}));
    el.dispatchEvent(new Event("change", {bubbles: true}));
  };
  const click = (el) => {
    el.scrollIntoView({block: "center", inline: "center"});
    el.click();
  };
  const clickByText = (pattern) => {
    for (const el of document.querySelectorAll("button,input[type=submit],a,div[role=button],span[role=button]")) {
      if (!el.disabled && visible(el) && pattern.test(textOf(el))) {
        click(el);
        return true;
      }
    }
    return false;
  };
  const firstVisible = (selectors) => {
    for (const selector of selectors) {
      for (const el of document.querySelectorAll(selector)) {
        if (visible(el)) return el;
      }
    }
    return null;
  };
  const selectValue = (selectors, value, labels = []) => {
    const el = firstVisible(selectors);
    if (!el) return false;
    const wanted = [value, String(value).padStart(2, "0"), ...labels].map((item) => String(item).trim().toLowerCase()).filter(Boolean);
    const option = Array.from(el.options || []).find((item) => {
      const candidates = [item.value, item.textContent, item.label].map((part) => String(part || "").trim().toLowerCase());
      return candidates.some((candidate) => wanted.includes(candidate));
    });
    el.value = option ? option.value : value;
    el.dispatchEvent(new Event("input", {bubbles: true}));
    el.dispatchEvent(new Event("change", {bubbles: true}));
    return true;
  };
  const controlText = (el) => [
    textOf(el),
    el.getAttribute?.("aria-label"),
    el.getAttribute?.("placeholder"),
    el.getAttribute?.("name"),
    el.getAttribute?.("id"),
    el.getAttribute?.("title")
  ].filter(Boolean).join(" ");
  const findControl = (patterns) => {
    for (const el of document.querySelectorAll("input,select,button,[role='button'],[role='combobox']")) {
      if (visible(el) && patterns.some((pattern) => pattern.test(controlText(el)))) return el;
    }
    return null;
  };
  const clickOption = async (values) => {
    const wanted = values.map((value) => String(value).trim().toLowerCase()).filter(Boolean);
    for (let attempt = 0; attempt < 8; attempt++) {
      const options = Array.from(document.querySelectorAll("[role='option'],option,li,button,div,span")).filter(visible);
      const option = options.find((el) => wanted.includes(textOf(el).trim().toLowerCase()));
      if (option) {
        click(option);
        await sleep(350);
        return true;
      }
      await sleep(150);
    }
    return false;
  };
  const setComboValue = async (patterns, values) => {
    const el = findControl(patterns);
    if (!el) return false;
    if (el.tagName === "SELECT") {
      const wanted = values.map((item) => String(item).trim().toLowerCase()).filter(Boolean);
      const option = Array.from(el.options || []).find((item) => [item.value, item.textContent, item.label].some((part) => wanted.includes(String(part || "").trim().toLowerCase())));
      el.value = option ? option.value : values[0];
      el.dispatchEvent(new Event("input", {bubbles: true}));
      el.dispatchEvent(new Event("change", {bubbles: true}));
      return true;
    }
    click(el);
    await sleep(350);
    return await clickOption(values);
  };
  const diagnostics = () => ({
    controls: Array.from(document.querySelectorAll("input,select,button,[role='button'],[role='combobox']")).filter(visible).slice(0, 30).map((el) => ({
      tag: el.tagName,
      type: el.getAttribute("type") || "",
      name: el.getAttribute("name") || "",
      id: el.id || "",
      aria: el.getAttribute("aria-label") || "",
      role: el.getAttribute("role") || "",
      text: textOf(el).slice(0, 80),
      value: el.value || ""
    }))
  });
  const bodyText = () => document.body ? document.body.innerText || "" : "";
  const current = () => ({url: location.href, text: bodyText()});
  const hasCaptcha = () => {
    const text = bodyText();
    return document.querySelector("iframe#enforcementFrame,[src*='arkoselabs'],[src*='funcaptcha'],[class*='captcha' i]") ||
      /captcha|challenge|press and hold|prove.*human|help us beat the robots|verify.*human/i.test(text);
  };
  const hasRateLimit = () => /unusual activity|temporarily blocked|try again later|too many attempts|service unavailable/i.test(bodyText());

  for (let i = 0; i < 1; i++) {
    const state = current();
    if (/outlook\.live\.com\/mail|mail\.live\.com/i.test(state.url) && /inbox|focused|other|new mail|outlook/i.test(state.text)) {
      return {state: "mailbox_opened", url: state.url};
    }
    if (/account\.live\.com\/abuse|identity|proof|recover|Add security info/i.test(state.url) || /help us protect|verify your identity|add security info|security info/i.test(state.text)) {
      return {state: "needs_manual_verification", url: state.url, message: "Microsoft requested extra verification"};
    }
    if (hasRateLimit()) return {state: "rate_limited", url: state.url, message: "Microsoft returned rate limit or unusual activity"};
    if (hasCaptcha()) return {state: "captcha_required", url: state.url, message: "CAPTCHA or press-and-hold challenge detected"};

    const consent = Array.from(document.querySelectorAll("button,input[type=submit]")).find((el) => visible(el) && /agree|accept|continue/i.test(textOf(el)));
    if (consent) {
      click(consent);
      await sleep(1200);
      continue;
    }

    const emailInput = firstVisible([
      "input[name='MemberName']",
      "input#MemberName",
      "input[type='email']",
      "input[name='loginfmt']",
      "input[aria-label*='email' i]",
      "input[placeholder*='email' i]",
      "input[type='text']"
    ]);
    if (emailInput && !/password|passwd|first|last|birth/i.test(emailInput.name || "")) {
      const signupText = bodyText();
      const value = /@outlook\.com|@hotmail\.com|new email/i.test(signupText) ? emailLocal : email;
      fill(emailInput, value);
      await sleep(400);
      clickByText(/^(next|continue|下一步|继续)$/i) || clickByText(/create|sign up/i);
      await sleep(1500);
      continue;
    }

    const passwordInput = firstVisible(["input[type='password']", "input[name='Password']", "input[name='passwd']"]);
    if (passwordInput) {
      fill(passwordInput, password);
      await sleep(400);
      clickByText(/^(next|continue|下一步|继续)$/i);
      await sleep(1500);
      continue;
    }

    const firstInput = firstVisible(["input#firstNameInput", "input[name='FirstName']", "input[aria-label*='first' i]"]);
    const lastInput = firstVisible(["input#lastNameInput", "input[name='LastName']", "input[aria-label*='last' i]"]);
    if (firstInput || lastInput) {
      if (firstInput) fill(firstInput, firstName);
      if (lastInput) fill(lastInput, lastName);
      await sleep(400);
      clickByText(/^(next|continue|下一步|继续)$/i);
      await sleep(1500);
      continue;
    }

    const monthLabels = [["January", "Jan"], ["February", "Feb"], ["March", "Mar"], ["April", "Apr"], ["May"], ["June", "Jun"], ["July", "Jul"], ["August", "Aug"], ["September", "Sep"], ["October", "Oct"], ["November", "Nov"], ["December", "Dec"]];
    const monthIndex = Math.max(1, Math.min(12, Number(birthMonth) || 1));
    const monthValues = [birthMonth, String(monthIndex), String(monthIndex).padStart(2, "0"), ...monthLabels[monthIndex - 1]];
    const dayValues = [birthDay, String(Number(birthDay) || 12), String(Number(birthDay) || 12).padStart(2, "0")];
    const yearInput = firstVisible(["input[name='BirthYear']", "input#BirthYear", "input[aria-label*='year' i]", "input[placeholder*='year' i]"]);
    const hasBirthControl = document.querySelector("select[name='BirthMonth'],select[name='BirthDay'],select[aria-label*='month' i],select[aria-label*='day' i],[role='combobox'],button");
    if (yearInput || hasBirthControl || /birthdate|birth date|date of birth|Month\s+Day\s+Year/i.test(state.text)) {
      selectValue(["select[name='BirthMonth']", "select#BirthMonth", "select[aria-label*='month' i]"], birthMonth, monthValues);
      selectValue(["select[name='BirthDay']", "select#BirthDay", "select[aria-label*='day' i]"], birthDay, dayValues);
      await setComboValue([/month/i], monthValues);
      await setComboValue([/day/i], dayValues);
      if (yearInput) fill(yearInput, birthYear);
      await sleep(500);
      clickByText(/^(next|continue|下一步|继续)$/i);
      await sleep(1600);
      continue;
    }

    if (clickByText(/^(yes|no|skip|next|continue|accept|not now|以后再说|跳过|下一步|继续)$/i)) {
      await sleep(1400);
      continue;
    }

    await sleep(1000);
  }
  return {state: "timeout", url: location.href, message: bodyText().slice(0, 300), diagnostics: diagnostics()};
}`
