package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	abs "github.com/microsoft/kiota-abstractions-go"
	absauth "github.com/microsoft/kiota-abstractions-go/authentication"
	msgraphsdk "github.com/microsoftgraph/msgraph-sdk-go"
	msgraphgocore "github.com/microsoftgraph/msgraph-sdk-go-core"
	graphmodels "github.com/microsoftgraph/msgraph-sdk-go/models"
	graphusers "github.com/microsoftgraph/msgraph-sdk-go/users"
)

type staticAccessTokenProvider struct {
	token     string
	validator *absauth.AllowedHostsValidator
}

func newStaticAccessTokenProvider(token string) (*staticAccessTokenProvider, error) {
	validator, err := absauth.NewAllowedHostsValidatorErrorCheck([]string{"graph.microsoft.com"})
	if err != nil {
		return nil, err
	}
	return &staticAccessTokenProvider{token: token, validator: validator}, nil
}

func (p *staticAccessTokenProvider) GetAuthorizationToken(_ context.Context, _ *url.URL, _ map[string]interface{}) (string, error) {
	return strings.TrimSpace(p.token), nil
}

func (p *staticAccessTokenProvider) GetAllowedHostsValidator() *absauth.AllowedHostsValidator {
	return p.validator
}

func newGraphClient(accessToken string, httpClient *http.Client) (*msgraphsdk.GraphServiceClient, error) {
	tokenProvider, err := newStaticAccessTokenProvider(accessToken)
	if err != nil {
		return nil, err
	}
	authProvider := absauth.NewBaseBearerTokenAuthenticationProvider(tokenProvider)
	options := msgraphsdk.GetDefaultClientOptions()
	graphHTTPClient := msgraphgocore.GetDefaultClient(&options)
	if httpClient != nil && httpClient.Timeout > 0 {
		graphHTTPClient.Timeout = httpClient.Timeout
	}
	adapter, err := msgraphsdk.NewGraphRequestAdapterWithParseNodeFactoryAndSerializationWriterFactoryAndHttpClient(authProvider, nil, nil, graphHTTPClient)
	if err != nil {
		return nil, err
	}
	return msgraphsdk.NewGraphServiceClient(adapter), nil
}

func (w *MailWatcher) fetchOnceWithGraphSDK(ctx context.Context, accessToken string, limit int, receivedAfterNs int64) ([]graphMessage, error) {
	client, err := newGraphClient(accessToken, w.httpClient)
	if err != nil {
		return nil, err
	}
	top := int32(messageLimitValue(int32(limit), w.messageLimit))
	filter := ""
	if receivedAfterNs > 0 {
		filter = "receivedDateTime gt " + time.Unix(0, receivedAfterNs).UTC().Format(time.RFC3339Nano)
	}
	headers := abs.NewRequestHeaders()
	headers.Add("Prefer", `outlook.body-content-type="text"`)
	query := &graphusers.ItemMessagesRequestBuilderGetQueryParameters{
		Top:     &top,
		Orderby: []string{"receivedDateTime desc"},
		Select:  []string{"id", "internetMessageId", "subject", "from", "bodyPreview", "body", "toRecipients", "ccRecipients", "bccRecipients", "internetMessageHeaders", "receivedDateTime"},
	}
	if filter != "" {
		query.Filter = &filter
	}
	resp, err := client.Me().Messages().Get(ctx, &graphusers.ItemMessagesRequestBuilderGetRequestConfiguration{
		Headers:         headers,
		QueryParameters: query,
	})
	if err != nil {
		return nil, graphFetchErrorFromSDK(err)
	}
	if resp == nil {
		return []graphMessage{}, nil
	}
	return graphMessagesFromSDK(resp.GetValue()), nil
}

func graphFetchErrorFromSDK(err error) error {
	var apiErr abs.ApiErrorable
	if !errors.As(err, &apiErr) {
		return err
	}
	return &GraphFetchError{
		StatusCode: apiErr.GetStatusCode(),
		Body:       safeErrorString(err),
		RetryAfter: retryAfterFromHeaders(apiErr.GetResponseHeaders()),
	}
}

func retryAfterFromHeaders(headers *abs.ResponseHeaders) time.Duration {
	if headers == nil {
		return 0
	}
	for _, value := range headers.Get("Retry-After") {
		if delay := retryAfter(value); delay > 0 {
			return delay
		}
	}
	return 0
}

func safeErrorString(err error) (value string) {
	if err == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			value = "graph sdk error"
		}
	}()
	return err.Error()
}

func graphMessagesFromSDK(messages []graphmodels.Messageable) []graphMessage {
	out := make([]graphMessage, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		out = append(out, graphMessage{
			ID:                     stringValueFromPtr(message.GetId()),
			Subject:                stringValueFromPtr(message.GetSubject()),
			From:                   graphRecipientFromSDK(message.GetFrom()),
			BodyPreview:            stringValueFromPtr(message.GetBodyPreview()),
			Body:                   graphBodyFromSDK(message.GetBody()),
			ToRecipients:           graphRecipientsFromSDK(message.GetToRecipients()),
			CcRecipients:           graphRecipientsFromSDK(message.GetCcRecipients()),
			BccRecipients:          graphRecipientsFromSDK(message.GetBccRecipients()),
			InternetMessageHeaders: graphHeadersFromSDK(message.GetInternetMessageHeaders()),
			ReceivedDateTime:       graphTimeFromSDK(message.GetReceivedDateTime()),
		})
	}
	return out
}

func graphBodyFromSDK(body graphmodels.ItemBodyable) graphBody {
	if body == nil {
		return graphBody{}
	}
	return graphBody{Content: stringValueFromPtr(body.GetContent())}
}

func graphRecipientFromSDK(recipient graphmodels.Recipientable) graphRecipient {
	if recipient == nil || recipient.GetEmailAddress() == nil {
		return graphRecipient{}
	}
	return graphRecipient{EmailAddress: graphEmailAddress{Address: stringValueFromPtr(recipient.GetEmailAddress().GetAddress())}}
}

func graphRecipientsFromSDK(recipients []graphmodels.Recipientable) []graphRecipient {
	out := make([]graphRecipient, 0, len(recipients))
	for _, recipient := range recipients {
		out = append(out, graphRecipientFromSDK(recipient))
	}
	return out
}

func graphHeadersFromSDK(headers []graphmodels.InternetMessageHeaderable) []graphHeader {
	out := make([]graphHeader, 0, len(headers))
	for _, header := range headers {
		if header == nil {
			continue
		}
		out = append(out, graphHeader{
			Name:  stringValueFromPtr(header.GetName()),
			Value: stringValueFromPtr(header.GetValue()),
		})
	}
	return out
}

func graphTimeFromSDK(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func stringValueFromPtr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
