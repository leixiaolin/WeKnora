package wechatmp

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
)

var _ im.Adapter = (*Adapter)(nil)

type Adapter struct {
	token  string
	client *client
}

func NewAdapter(appID, appSecret, token, apiBaseURL string) (*Adapter, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("token is required")
	}
	client, err := newClient(appID, appSecret, apiBaseURL)
	if err != nil {
		return nil, err
	}
	return &Adapter{token: token, client: client}, nil
}

func (a *Adapter) Platform() im.Platform {
	return im.PlatformWeChatMP
}

func (a *Adapter) HandleURLVerification(c *gin.Context) bool {
	if c.Request.Method != http.MethodGet || c.Query("echostr") == "" {
		return false
	}
	if err := a.VerifyCallback(c); err != nil {
		logger.Warnf(c.Request.Context(), "[WeChatMP] URL verification failed: %v", err)
		c.String(http.StatusForbidden, "verification failed")
		return true
	}
	c.String(http.StatusOK, c.Query("echostr"))
	return true
}

func (a *Adapter) VerifyCallback(c *gin.Context) error {
	if !verifySignatureAt(
		a.token,
		c.Query("signature"),
		c.Query("timestamp"),
		c.Query("nonce"),
		time.Now(),
	) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

func (a *Adapter) ParseCallback(c *gin.Context) (*im.IncomingMessage, error) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	var msg incomingXMLMessage
	if err := xml.Unmarshal(bodyBytes, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal xml: %w", err)
	}

	msgType := strings.ToLower(strings.TrimSpace(msg.MsgType))
	switch msgType {
	case "text":
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			return nil, nil
		}
		extra := map[string]string{
			"to_user_name": msg.ToUserName,
			"create_time":  fmt.Sprintf("%d", msg.CreateTime),
		}
		return &im.IncomingMessage{
			Platform:    im.PlatformWeChatMP,
			MessageType: im.MessageTypeText,
			UserID:      msg.FromUserName,
			UserName:    msg.FromUserName,
			ChatType:    im.ChatTypeDirect,
			Content:     content,
			MessageID:   msg.MsgID,
			Extra:       extra,
		}, nil
	case "event", "image", "voice", "video", "location", "link":
		logger.Infof(c.Request.Context(), "[WeChatMP] Ignoring unsupported message type: %s event=%s",
			msg.MsgType, msg.Event)
		return nil, nil
	default:
		logger.Infof(c.Request.Context(), "[WeChatMP] Ignoring unknown message type: %s", msg.MsgType)
		return nil, nil
	}
}

func (a *Adapter) SendReply(ctx context.Context, incoming *im.IncomingMessage, reply *im.ReplyMessage) error {
	if incoming == nil || strings.TrimSpace(incoming.UserID) == "" {
		return fmt.Errorf("missing openid")
	}
	if reply == nil {
		return nil
	}

	text := simplifyForWeChat(reply.Content)
	chunks := splitForWeChat(text, defaultWechatTextChunkLimit)
	for i, chunk := range chunks {
		if err := a.client.sendCustomTextMessage(ctx, incoming.UserID, chunk); err != nil {
			return err
		}
		if i < len(chunks)-1 {
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil
}
