package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/gin-gonic/gin"
)

func TestApplyIMChannelModeDefaults(t *testing.T) {
	tests := []struct {
		name       string
		platform   string
		wantMode   string
		wantOutput string
	}{
		{"wechat personal", "wechat", "longpoll", "full"},
		{"wechat mp", "wechat_mp", "webhook", "full"},
		{"mattermost", "mattermost", "webhook", "stream"},
		{"slack", "slack", "websocket", "stream"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := &im.IMChannel{Platform: tt.platform}
			applyIMChannelModeDefaults(ch)
			if ch.Mode != tt.wantMode || ch.OutputMode != tt.wantOutput {
				t.Fatalf("mode/output = %q/%q, want %q/%q",
					ch.Mode, ch.OutputMode, tt.wantMode, tt.wantOutput)
			}
		})
	}
}

func TestWriteIMCallbackAck(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("wechat mp empty body", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		writeIMCallbackAck(c, "wechat_mp")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if w.Body.String() != "" {
			t.Fatalf("body = %q, want empty", w.Body.String())
		}
	})

	t.Run("other platform json", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		writeIMCallbackAck(c, "slack")
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
		}
		if w.Body.String() == "" {
			t.Fatal("body is empty, want JSON ack")
		}
	})
}
