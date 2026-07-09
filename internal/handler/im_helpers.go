package handler

import (
	"net/http"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/gin-gonic/gin"
)

func applyIMChannelModeDefaults(channel *im.IMChannel) {
	switch channel.Platform {
	case "wechat":
		channel.Mode = "longpoll"
		channel.OutputMode = "full"
	case "wechat_mp":
		channel.Mode = "webhook"
		channel.OutputMode = "full"
	default:
		if channel.Mode == "" {
			if channel.Platform == "mattermost" {
				channel.Mode = "webhook"
			} else {
				channel.Mode = "websocket"
			}
		}
		if channel.OutputMode == "" {
			channel.OutputMode = "stream"
		}
	}
}

func writeIMCallbackAck(c *gin.Context, platform string) {
	if platform == "wechat_mp" {
		c.String(http.StatusOK, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
