package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	deepLinkVersion         = "v1"
	defaultDeepLinkTTLHours = 720
	channelContextKeyPrefix = "spb:ctx:"
	userContextKeyPrefix    = "spb:user:"
	activeContextKeyPostfix = ":active"
	minDeepLinkPayloadParts = 4
)

type ChannelContextStore struct {
	client *redis.Client
	ttl    time.Duration
}

func NewChannelContextStore(addr, password string, db int, ttl time.Duration) *ChannelContextStore {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	return &ChannelContextStore{
		client: client,
		ttl:    ttl,
	}
}

func (s *ChannelContextStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *ChannelContextStore) Close() error {
	return s.client.Close()
}

func (s *ChannelContextStore) ActivateChannelForUser(ctx context.Context, userID int64, channelID int64) (string, error) {
	key, err := generateContextKey()
	if err != nil {
		return "", err
	}

	channelKey := channelContextRedisKey(key)
	userActiveKey := userActiveContextRedisKey(userID)
	channelValue := strconv.FormatInt(channelID, 10)

	pipe := s.client.TxPipeline()
	pipe.Set(ctx, channelKey, channelValue, s.ttl)
	pipe.Set(ctx, userActiveKey, key, s.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", err
	}

	return key, nil
}

func (s *ChannelContextStore) GetActiveChannelForUser(ctx context.Context, userID int64) (int64, bool, error) {
	userActiveKey := userActiveContextRedisKey(userID)
	ctxKey, err := s.client.Get(ctx, userActiveKey).Result()
	if err == redis.Nil {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}

	channelKey := channelContextRedisKey(ctxKey)
	channelRaw, err := s.client.Get(ctx, channelKey).Result()
	if err == redis.Nil {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}

	channelID, err := strconv.ParseInt(channelRaw, 10, 64)
	if err != nil {
		return 0, false, err
	}

	return channelID, true, nil
}

func ParseAndVerifyDeepLinkPayload(payload, secret string, maxAge time.Duration) (int64, error) {
	parts := strings.Split(payload, ".")
	if len(parts) != minDeepLinkPayloadParts {
		return 0, fmt.Errorf("invalid payload format")
	}
	if parts[0] != deepLinkVersion {
		return 0, fmt.Errorf("unsupported payload version")
	}

	channelID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid channel id in payload")
	}

	issuedAtUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid timestamp in payload")
	}

	issuedAt := time.Unix(issuedAtUnix, 0)
	if time.Since(issuedAt) > maxAge {
		return 0, fmt.Errorf("payload expired")
	}

	expectedSig := signDeepLinkPayload(channelID, issuedAtUnix, secret)
	if !hmac.Equal([]byte(expectedSig), []byte(parts[3])) {
		return 0, fmt.Errorf("invalid payload signature")
	}

	return channelID, nil
}

func BuildDeepLinkPayload(channelID int64, issuedAt time.Time, secret string) string {
	issuedAtUnix := issuedAt.Unix()
	signature := signDeepLinkPayload(channelID, issuedAtUnix, secret)
	return fmt.Sprintf("%s.%d.%d.%s", deepLinkVersion, channelID, issuedAtUnix, signature)
}

func signDeepLinkPayload(channelID int64, issuedAtUnix int64, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(fmt.Sprintf("%s|%d|%d", deepLinkVersion, channelID, issuedAtUnix)))
	return hex.EncodeToString(mac.Sum(nil))
}

func generateContextKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func channelContextRedisKey(contextKey string) string {
	return channelContextKeyPrefix + contextKey
}

func userActiveContextRedisKey(userID int64) string {
	return userContextKeyPrefix + strconv.FormatInt(userID, 10) + activeContextKeyPostfix
}
