package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

func localManualRecoveryProxyURL() string {
	return strings.TrimSpace(envDefault("MAILBOX_LOCAL_RECOVERY_PROXY_URL", "socks5://127.0.0.1:10811"))
}

func localOutlookOAuthAuthorizeURL(email string) string {
	clientID := strings.TrimSpace(envDefault("MAILBOX_LOCAL_OAUTH_CLIENT_ID", "9e5f94bc-e8a4-4e73-b8be-63364c29d753"))
	redirectURL := strings.TrimSpace(envDefault("MAILBOX_LOCAL_OAUTH_REDIRECT_URL", "https://login.microsoftonline.com/common/oauth2/nativeclient"))
	scopes := strings.TrimSpace(envDefault("MAILBOX_LOCAL_OAUTH_SCOPES", "offline_access https://graph.microsoft.com/Mail.Read"))
	baseURL := strings.TrimSpace(envDefault("MAILBOX_LOCAL_OAUTH_AUTHORIZE_URL", "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"))
	values := url.Values{}
	values.Set("client_id", clientID)
	values.Set("response_type", "code")
	values.Set("redirect_uri", redirectURL)
	values.Set("response_mode", "query")
	values.Set("scope", scopes)
	values.Set("login_hint", normalizeEmail(email))
	values.Set("state", "local-"+shortValueHash(fmt.Sprintf("%s:%d", email, time.Now().UnixNano())))
	return baseURL + "?" + values.Encode()
}

func psSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func localManualRecoveryLaunchCommand(email string, proxyURL string, recoveryURL string) string {
	profileName := fmt.Sprintf("mailbox-recovery-%s", shortValueHash(email))
	pythonPath := strings.TrimSpace(envDefault("MAILBOX_LOCAL_RECOVERY_PYTHON", `D:\DevProjects\Gopay_plus_automatic\.venv312\Scripts\python.exe`))
	scriptPath := strings.TrimSpace(envDefault("MAILBOX_LOCAL_RECOVERY_SCRIPT", `D:\DevProjects\mailbox\tools\manual_recovery_chrome.py`))
	clientID := strings.TrimSpace(envDefault("MAILBOX_LOCAL_OAUTH_CLIENT_ID", "9e5f94bc-e8a4-4e73-b8be-63364c29d753"))
	redirectURL := strings.TrimSpace(envDefault("MAILBOX_LOCAL_OAUTH_REDIRECT_URL", "https://login.microsoftonline.com/common/oauth2/nativeclient"))
	scopes := strings.TrimSpace(envDefault("MAILBOX_LOCAL_OAUTH_SCOPES", "offline_access https://graph.microsoft.com/Mail.Read"))
	completeURL := strings.TrimSpace(envDefault("MAILBOX_LOCAL_OAUTH_COMPLETE_URL", "http://127.0.0.1:8080/api/mailboxes/oauth-local/complete"))
	args := []string{
		scriptPath,
		"--proxy",
		proxyURL,
		"--profile-dir",
		"$profileDir",
		"--url",
		recoveryURL,
		"--email",
		normalizeEmail(email),
		"--authorize-url",
		localOutlookOAuthAuthorizeURL(email),
		"--client-id",
		clientID,
		"--redirect-uri",
		redirectURL,
		"--scope",
		scopes,
		"--complete-url",
		completeURL,
		"--hold-seconds",
		strings.TrimSpace(envDefault("MAILBOX_LOCAL_RECOVERY_HOLD_SECONDS", "7200")),
	}
	quotedArgs := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "$profileDir" {
			quotedArgs = append(quotedArgs, "$profileDir")
			continue
		}
		quotedArgs = append(quotedArgs, psSingleQuote(arg))
	}
	return fmt.Sprintf("$profileDir = Join-Path $env:TEMP %s; & %s %s", psSingleQuote(profileName), psSingleQuote(pythonPath), strings.Join(quotedArgs, " "))
}
