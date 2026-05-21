package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"mailboxapi/pb"
)

const defaultCloudflareAPIBaseURL = "https://api.cloudflare.com/client/v4"

func loadCloudflareEmailDomains() []string {
	cfg := loadCloudflareEmailConfig()
	token := strings.TrimSpace(os.Getenv("MAILBOX_CLOUDFLARE_API_TOKEN"))
	if token == "" {
		if cfg != nil && len(cfg.GetZones()) > 0 {
			logWarning("MAILBOX_CLOUDFLARE_API_TOKEN is required to load Cloudflare email domains")
		}
		return nil
	}
	timeout := time.Duration(envInt("MAILBOX_CLOUDFLARE_API_TIMEOUT_SECONDS", defaultHTTPTimeoutSeconds)) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	domains, err := fetchCloudflareEmailDomains(ctx, &http.Client{Timeout: timeout}, token, cfg)
	if err != nil {
		logWarning("fetch Cloudflare email config: %v", err)
		return nil
	}
	if len(domains) == 0 {
		logWarning("Cloudflare email API returned no mailbox domains")
		return nil
	}
	logInfo("loaded Cloudflare email domains from API count=%d", len(domains))
	return domains
}

func loadCloudflareEmailConfig() *pb.CloudflareEmailConfig {
	path := strings.TrimSpace(os.Getenv("MAILBOX_CLOUDFLARE_EMAIL_CONFIG_FILE"))
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		logWarning("read Cloudflare email config: %v", err)
		return nil
	}
	var cfg pb.CloudflareEmailConfig
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &cfg); err != nil {
		logWarning("decode Cloudflare email config: %v", err)
		return nil
	}
	return &cfg
}

func fetchCloudflareEmailDomains(ctx context.Context, client *http.Client, token string, cfg *pb.CloudflareEmailConfig) ([]string, error) {
	if cfg == nil || len(cfg.GetZones()) == 0 {
		return nil, nil
	}
	api := &cloudflareEmailAPI{
		client:  client,
		token:   token,
		baseURL: strings.TrimRight(envStr("MAILBOX_CLOUDFLARE_API_BASE_URL", cfg.GetApiBaseUrl()), "/"),
	}
	if api.baseURL == "" {
		api.baseURL = defaultCloudflareAPIBaseURL
	}
	out := []string{}
	seen := map[string]struct{}{}
	for _, zone := range cfg.GetZones() {
		if !optionalBoolEnabled(zone.Enabled) {
			continue
		}
		if !optionalBoolEnabled(zone.CatchAllEnabled) {
			continue
		}
		zoneID, zoneName, err := api.resolveZone(ctx, zone)
		if err != nil {
			return nil, err
		}
		catchAll, err := api.emailRoutingCatchAll(ctx, zoneID)
		if err != nil {
			return nil, err
		}
		workerName := strings.TrimSpace(zone.GetWorkerName())
		if !catchAll.WorkerEnabled(workerName) {
			continue
		}
		mxDomains, err := api.emailRoutingMXDomains(ctx, zoneID)
		if err != nil {
			return nil, err
		}
		added := false
		for _, domain := range mxDomains {
			added = appendCloudflareEmailDomain(&out, seen, domain) || added
		}
		if !added {
			appendCloudflareEmailDomain(&out, seen, zoneName)
		}
	}
	return out, nil
}

type cloudflareEmailAPI struct {
	client  *http.Client
	token   string
	baseURL string
}

type cloudflareResponse[T any] struct {
	Success bool                `json:"success"`
	Errors  []cloudflareError   `json:"errors"`
	Result  T                   `json:"result"`
	Info    *cloudflarePageInfo `json:"result_info"`
}

type cloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cloudflarePageInfo struct {
	TotalPages int `json:"total_pages"`
}

type cloudflareZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cloudflareEmailAction struct {
	Type  string                `json:"type"`
	Value cloudflareStringSlice `json:"value"`
}

type cloudflareEmailCatchAll struct {
	Enabled bool                    `json:"enabled"`
	Actions []cloudflareEmailAction `json:"actions"`
}

type cloudflareDNSRecord struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type cloudflareStringSlice []string

func (values *cloudflareStringSlice) UnmarshalJSON(raw []byte) error {
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		*values = list
		return nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err != nil {
		return err
	}
	*values = []string{single}
	return nil
}

func (api *cloudflareEmailAPI) resolveZone(ctx context.Context, zone *pb.CloudflareEmailZone) (string, string, error) {
	zoneID := strings.TrimSpace(zone.GetZoneId())
	zoneName := normalizeCloudflareDomain(zone.GetZoneName())
	if zoneID != "" {
		return zoneID, zoneName, nil
	}
	if zoneName == "" {
		return "", "", fmt.Errorf("Cloudflare email zone requires zone_id or zone_name")
	}
	values := url.Values{}
	values.Set("name", zoneName)
	zones, err := requestCloudflare[[]cloudflareZone](ctx, api, "/zones?"+values.Encode())
	if err != nil {
		return "", "", err
	}
	if len(zones) == 0 {
		return "", "", fmt.Errorf("Cloudflare zone not found: %s", zoneName)
	}
	return zones[0].ID, zones[0].Name, nil
}

func (api *cloudflareEmailAPI) emailRoutingCatchAll(ctx context.Context, zoneID string) (cloudflareEmailCatchAll, error) {
	return requestCloudflare[cloudflareEmailCatchAll](ctx, api, "/zones/"+url.PathEscape(zoneID)+"/email/routing/rules/catch_all")
}

func (api *cloudflareEmailAPI) emailRoutingMXDomains(ctx context.Context, zoneID string) ([]string, error) {
	values := url.Values{}
	values.Set("type", "MX")
	values.Set("per_page", "100")
	records, err := requestCloudflare[[]cloudflareDNSRecord](ctx, api, "/zones/"+url.PathEscape(zoneID)+"/dns_records?"+values.Encode())
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, record := range records {
		content := strings.ToLower(strings.TrimSpace(record.Content))
		if !strings.HasSuffix(content, ".mx.cloudflare.net") {
			continue
		}
		out = append(out, record.Name)
	}
	return out, nil
}

func requestCloudflare[T any](ctx context.Context, api *cloudflareEmailAPI, path string) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api.baseURL+path, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("Authorization", "Bearer "+api.token)
	req.Header.Set("Accept", "application/json")
	resp, err := api.client.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	var decoded cloudflareResponse[T]
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return zero, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !decoded.Success {
		return zero, fmt.Errorf("Cloudflare API %s failed status=%d errors=%s", path, resp.StatusCode, cloudflareErrorsString(decoded.Errors))
	}
	return decoded.Result, nil
}

func (rule cloudflareEmailCatchAll) WorkerEnabled(workerName string) bool {
	if !rule.Enabled {
		return false
	}
	for _, action := range rule.Actions {
		if strings.TrimSpace(strings.ToLower(action.Type)) != "worker" {
			continue
		}
		if workerName == "" {
			return true
		}
		for _, value := range action.Value {
			if strings.EqualFold(strings.TrimSpace(value), workerName) {
				return true
			}
		}
	}
	return false
}

func optionalBoolEnabled(value *bool) bool {
	return value == nil || *value
}

func appendCloudflareEmailDomain(out *[]string, seen map[string]struct{}, value string) bool {
	domain := normalizeCloudflareDomain(value)
	if domain == "" {
		return false
	}
	if _, ok := seen[domain]; ok {
		return false
	}
	seen[domain] = struct{}{}
	*out = append(*out, domain)
	return true
}

func normalizeCloudflareDomain(value string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(value)), ".")
}

func cloudflareErrorsString(errors []cloudflareError) string {
	if len(errors) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(errors))
	for _, item := range errors {
		parts = append(parts, strings.TrimSpace(item.Message))
	}
	return strings.Join(parts, "; ")
}
