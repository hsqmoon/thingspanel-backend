package dal

import (
	"context"
	"errors"
	"net"
	"testing"

	"project/pkg/global"

	"github.com/redis/go-redis/v9"
)

func TestVerifyOpenAPIKeyReturnsRedisOutageInsteadOfTreatingItAsCacheMiss(t *testing.T) {
	previous := global.REDIS
	global.REDIS = redis.NewClient(&redis.Options{
		Addr: "redis.invalid:6379",
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("injected redis outage")
		},
	})
	t.Cleanup(func() {
		_ = global.REDIS.Close()
		global.REDIS = previous
	})

	_, _, err := VerifyOpenAPIKey(context.Background(), "key")
	if err == nil {
		t.Fatal("expected Redis outage error")
	}
}
