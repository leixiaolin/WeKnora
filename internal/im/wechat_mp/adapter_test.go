package wechatmp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/gin-gonic/gin"
)

func TestVerifySignatureAt(t *testing.T) {
	now := time.Now()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	nonce := "nonce-1"
	token := "secret-token"
	signature := computeSignature(token, timestamp, nonce)

	if !verifySignatureAt(token, signature, timestamp, nonce, now) {
		t.Fatal("valid signature rejected")
	}
	if verifySignatureAt(token, signature, timestamp, "tampered", now) {
		t.Fatal("tampered nonce accepted")
	}
	oldTimestamp := strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10)
	oldSignature := computeSignature(token, oldTimestamp, nonce)
	if verifySignatureAt(token, oldSignature, oldTimestamp, nonce, now) {
		t.Fatal("stale timestamp accepted")
	}
}

func TestHandleURLVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adapter := newTestAdapter(t)
	now := time.Now()
	values := signedQuery(adapter.token, now, "n1")
	values.Set("echostr", "hello-wechat")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/callback?"+values.Encode(), nil)

	if !adapter.HandleURLVerification(c) {
		t.Fatal("verification request was not handled")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if body := w.Body.String(); body != "hello-wechat" {
		t.Fatalf("body = %q, want echostr", body)
	}
}

func TestParseCallback_TextAndBodyRewind(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adapter := newTestAdapter(t)
	body := `<xml>
<ToUserName><![CDATA[gh_test]]></ToUserName>
<FromUserName><![CDATA[openid_123]]></FromUserName>
<CreateTime>1710000000</CreateTime>
<MsgType><![CDATA[text]]></MsgType>
<Content><![CDATA[ hello ]]></Content>
<MsgId>10001</MsgId>
</xml>`

	c := testPostContext(body)
	msg, err := adapter.ParseCallback(c)
	if err != nil {
		t.Fatalf("ParseCallback error: %v", err)
	}
	if msg.Platform != im.PlatformWeChatMP {
		t.Fatalf("Platform = %q, want %q", msg.Platform, im.PlatformWeChatMP)
	}
	if msg.UserID != "openid_123" || msg.Content != "hello" || msg.MessageID != "10001" {
		t.Fatalf("parsed msg = %+v", msg)
	}
	if msg.ChatType != im.ChatTypeDirect || msg.MessageType != im.MessageTypeText {
		t.Fatalf("unexpected chat/message type: %+v", msg)
	}

	rewound, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatalf("read rewound body: %v", err)
	}
	if string(rewound) != body {
		t.Fatal("request body was not rewound")
	}
}

func TestParseCallback_UnsupportedEventReturnsNil(t *testing.T) {
	adapter := newTestAdapter(t)
	body := `<xml>
<ToUserName><![CDATA[gh_test]]></ToUserName>
<FromUserName><![CDATA[openid_123]]></FromUserName>
<CreateTime>1710000000</CreateTime>
<MsgType><![CDATA[event]]></MsgType>
<Event><![CDATA[subscribe]]></Event>
</xml>`

	msg, err := adapter.ParseCallback(testPostContext(body))
	if err != nil {
		t.Fatalf("ParseCallback error: %v", err)
	}
	if msg != nil {
		t.Fatalf("msg = %+v, want nil", msg)
	}
}

func TestSimplifyForWeChat(t *testing.T) {
	input := "# 标题\n\n> 引用\n\n```go\nfmt.Println(1)\n```\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n[链接](https://example.com) **加粗** ![图](x)"
	got := simplifyForWeChat(input)
	for _, want := range []string{"【标题】", "｜引用", "    fmt.Println(1)", "A | B", "1 | 2", "链接", "加粗", "[图片] 图"} {
		if !strings.Contains(got, want) {
			t.Fatalf("simplified text missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "---") || strings.Contains(got, "https://example.com") {
		t.Fatalf("simplified text retained markdown noise:\n%s", got)
	}
}

func TestSplitForWeChat(t *testing.T) {
	text := strings.Repeat("你", 551)
	chunks := splitForWeChat(text, 550)
	if len(chunks) != 2 {
		t.Fatalf("chunks len = %d, want 2", len(chunks))
	}
	if got := len([]rune(chunks[0])); got != 550 {
		t.Fatalf("first chunk runes = %d, want 550", got)
	}
}

func TestClientSendCustomTextMessage_RetriesExpiredToken(t *testing.T) {
	var tokenCalls atomic.Int32
	var sendCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/token":
			tokenCalls.Add(1)
			_ = json.NewEncoder(w).Encode(tokenResponse{
				AccessToken: "token-" + strconv.Itoa(int(tokenCalls.Load())),
				ExpiresIn:   7200,
			})
		case "/cgi-bin/message/custom/send":
			sendCalls.Add(1)
			if r.URL.Query().Get("access_token") == "token-1" {
				_ = json.NewEncoder(w).Encode(apiResponse{ErrCode: 42001, ErrMsg: "expired"})
				return
			}
			var payload customTextMessage
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode payload: %v", err)
			}
			if payload.ToUser != "openid" || payload.Text.Content != "hello" {
				t.Errorf("payload = %+v", payload)
			}
			_ = json.NewEncoder(w).Encode(apiResponse{ErrCode: 0})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := &client{
		appID:     "app",
		appSecret: "secret",
		baseURL:   server.URL,
		http:      server.Client(),
	}
	if err := c.sendCustomTextMessage(context.Background(), "openid", "hello"); err != nil {
		t.Fatalf("sendCustomTextMessage error: %v", err)
	}
	if tokenCalls.Load() != 2 {
		t.Fatalf("token calls = %d, want 2", tokenCalls.Load())
	}
	if sendCalls.Load() != 2 {
		t.Fatalf("send calls = %d, want 2", sendCalls.Load())
	}
}

func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	return &Adapter{
		token: "secret-token",
		client: &client{
			appID:     "app",
			appSecret: "secret",
			baseURL:   defaultAPIBaseURL,
			http:      defaultHTTPClient,
		},
	}
}

func signedQuery(token string, now time.Time, nonce string) url.Values {
	timestamp := strconv.FormatInt(now.Unix(), 10)
	values := url.Values{}
	values.Set("timestamp", timestamp)
	values.Set("nonce", nonce)
	values.Set("signature", computeSignature(token, timestamp, nonce))
	return values
}

func testPostContext(body string) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/callback", bytes.NewBufferString(body))
	return c
}
