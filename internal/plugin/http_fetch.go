package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	rayleabot "github.com/RayleaBot/RayleaBot/sdk/go"
)

const (
	defaultHTTPTimeout      = 10 * time.Second
	maxHTTPResponseBodySize = 4 << 20
)

var errHTTPResponseTooLarge = errors.New("http response body exceeds 4 MiB")

// httpRequest keeps the request shape of the host http.request action that
// plugin protocol v4 removed.
type httpRequest struct {
	Method         string
	URL            string
	Headers        map[string]string
	TimeoutSeconds int
	BodyText       string
	BodyBase64     string
}

// httpClient leaves redirects to the caller, as the host action did.
var httpClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// runtimeActions adds the plugin's own HTTP client to the SDK actions.
type runtimeActions struct {
	*rayleabot.Actions
}

func (runtimeActions) HTTPRequest(ctx context.Context, request httpRequest) (rayleabot.ActionResult, error) {
	return fetchHTTP(ctx, request)
}

// fetchHTTP sends the request with the plugin's own client. The result keeps
// the host action fields (status_code, headers, set_cookies and body_text or
// body_base64) so response parsing is unchanged. GET and HEAD retry once after a
// transport error or a 408, 429, 502, 503 or 504 response.
func fetchHTTP(ctx context.Context, request httpRequest) (rayleabot.ActionResult, error) {
	if request.BodyText != "" && request.BodyBase64 != "" {
		return nil, errors.New("http request accepts at most one body representation")
	}
	body := []byte(request.BodyText)
	if request.BodyBase64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(request.BodyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode http request body: %w", err)
		}
		body = decoded
	}
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if method == "" {
		method = http.MethodGet
	}
	timeout := defaultHTTPTimeout
	if request.TimeoutSeconds > 0 {
		timeout = time.Duration(request.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, retry, err := sendHTTP(ctx, method, request, body)
	if retry && (method == http.MethodGet || method == http.MethodHead) && ctx.Err() == nil {
		result, _, err = sendHTTP(ctx, method, request, body)
	}
	return result, err
}

func sendHTTP(ctx context.Context, method string, request httpRequest, body []byte) (rayleabot.ActionResult, bool, error) {
	outgoing, err := http.NewRequestWithContext(ctx, method, request.URL, bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("build http request: %w", err)
	}
	for key, value := range request.Headers {
		if !strings.EqualFold(key, "Accept-Encoding") {
			outgoing.Header.Set(key, value)
		}
	}
	response, err := httpClient.Do(outgoing)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = response.Body.Close() }()
	if method != http.MethodHead && response.ContentLength > maxHTTPResponseBodySize {
		return nil, false, errHTTPResponseTooLarge
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPResponseBodySize+1))
	if err != nil {
		return nil, true, err
	}
	if len(content) > maxHTTPResponseBodySize {
		return nil, false, errHTTPResponseTooLarge
	}

	headers := make(map[string]any, len(response.Header))
	for key, values := range response.Header {
		headers[key] = strings.Join(values, ", ")
	}
	cookies := make([]any, 0)
	for _, cookie := range response.Header.Values("Set-Cookie") {
		cookies = append(cookies, cookie)
	}
	result := rayleabot.ActionResult{"status_code": response.StatusCode, "headers": headers, "set_cookies": cookies}
	if len(content) > 0 {
		if utf8.Valid(content) {
			result["body_text"] = string(content)
		} else {
			result["body_base64"] = base64.StdEncoding.EncodeToString(content)
		}
	}
	switch response.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return result, true, nil
	}
	return result, false, nil
}
