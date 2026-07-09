package wechatmp

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/im"
)

func NewFactory() im.AdapterFactory {
	return func(_ context.Context, channel *im.IMChannel, _ func(context.Context, *im.IncomingMessage) error) (im.Adapter, context.CancelFunc, error) {
		if mode := im.ResolveMode(channel, "webhook"); mode != "webhook" {
			return nil, nil, fmt.Errorf("wechat_mp only supports webhook mode, got %s", mode)
		}
		creds, err := im.ParseCredentials(channel.Credentials)
		if err != nil {
			return nil, nil, fmt.Errorf("parse wechat_mp credentials: %w", err)
		}
		adapter, err := NewAdapter(
			im.GetString(creds, "app_id"),
			im.GetString(creds, "app_secret"),
			im.GetString(creds, "token"),
			im.GetString(creds, "api_base_url"),
		)
		if err != nil {
			return nil, nil, err
		}
		return adapter, nil, nil
	}
}
