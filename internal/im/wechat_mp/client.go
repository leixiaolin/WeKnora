package wechatmp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

const (
	defaultAPIBaseURL = "https://api.weixin.qq.com"
	tokenSafetyMargin = 5 * time.Minute
)

var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

type client struct {
	appID     string
	appSecret string
	baseURL   string
	http      *http.Client

	tokenMu    sync.Mutex
	tokenCache string
	tokenExpAt time.Time
}

func newClient(appID, appSecret, baseURL string) (*client, error) {
	appID = strings.TrimSpace(appID)
	appSecret = strings.TrimSpace(appSecret)
	if appID == "" {
		return nil, fmt.Errorf("app_id is required")
	}
	if appSecret == "" {
		return nil, fmt.Errorf("app_secret is required")
	}
	if baseURL == "" {
		baseURL = defaultAPIBaseURL
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if err := validateAPIBaseURL(baseURL); err != nil {
		return nil, err
	}
	return &client{
		appID:     appID,
		appSecret: appSecret,
		baseURL:   baseURL,
		http:      defaultHTTPClient,
	}, nil
}

func validateAPIBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid api_base_url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("api_base_url must use https scheme")
	}
	if u.Hostname() == "" {
		return fmt.Errorf("api_base_url must include host")
	}
	if raw != defaultAPIBaseURL {
		if err := secutils.ValidateURLForSSRF(raw); err != nil {
			return fmt.Errorf("invalid api_base_url: %w", err)
		}
	}
	return nil
}

func (c *client) getAccessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.tokenCache != "" && time.Now().Before(c.tokenExpAt) {
		return c.tokenCache, nil
	}

	tokenURL := c.baseURL + "/cgi-bin/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	q := req.URL.Query()
	q.Set("grant_type", "client_credential")
	q.Set("appid", c.appID)
	q.Set("secret", c.appSecret)
	req.URL.RawQuery = q.Encode()

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("request access token: %w", err)
	}
	defer resp.Body.Close()

	var result tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if result.ErrCode != 0 {
		return "", fmt.Errorf("get token error: code=%d msg=%s", result.ErrCode, result.ErrMsg)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("get token error: empty access_token")
	}

	ttl := time.Duration(result.ExpiresIn) * time.Second
	if ttl > tokenSafetyMargin {
		ttl -= tokenSafetyMargin
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	c.tokenCache = result.AccessToken
	c.tokenExpAt = time.Now().Add(ttl)
	return c.tokenCache, nil
}

func (c *client) clearTokenCache() {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.tokenCache = ""
	c.tokenExpAt = time.Time{}
}

func (c *client) sendCustomTextMessage(ctx context.Context, openID, content string) error {
	return c.sendCustomTextMessageWithRetry(ctx, openID, content, true)
}

func (c *client) sendCustomTextMessageWithRetry(ctx context.Context, openID, content string, retryToken bool) error {
	accessToken, err := c.getAccessToken(ctx)
	if err != nil {
		return err
	}

	payload := customTextMessage{
		ToUser:  openID,
		MsgType: "text",
	}
	payload.Text.Content = content

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal custom message: %w", err)
	}

	sendURL := c.baseURL + "/cgi-bin/message/custom/send"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sendURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create custom message request: %w", err)
	}
	q := req.URL.Query()
	q.Set("access_token", accessToken)
	req.URL.RawQuery = q.Encode()
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send custom message: %w", err)
	}
	defer resp.Body.Close()

	var result apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode custom message response: %w", err)
	}
	switch result.ErrCode {
	case 0:
		return nil
	case 40001, 42001:
		if retryToken {
			c.clearTokenCache()
			return c.sendCustomTextMessageWithRetry(ctx, openID, content, false)
		}
		return fmt.Errorf("wechat mp token error after retry: code=%d msg=%s", result.ErrCode, result.ErrMsg)
	case 45015:
		logger.Warnf(ctx, "[WeChatMP] Cannot send custom message outside customer-service window: openid=%s", openID)
		return nil
	case 45009:
		logger.Warnf(ctx, "[WeChatMP] Custom message rate limit hit: openid=%s", openID)
		return nil
	default:
		return fmt.Errorf("wechat mp custom message error: code=%d msg=%s", result.ErrCode, result.ErrMsg)
	}
}
