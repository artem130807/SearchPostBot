package main

import (
	"testing"
	"time"
)

func TestBuildAndParseDeepLinkPayload(t *testing.T) {
	secret := "test-secret"
	channelID := int64(-1001234567890)
	issuedAt := time.Now().Add(-time.Minute)

	payload := BuildDeepLinkPayload(channelID, issuedAt, secret)
	gotChannelID, err := ParseAndVerifyDeepLinkPayload(payload, secret, 2*time.Hour)
	if err != nil {
		t.Fatalf("expected valid payload, got error: %v", err)
	}
	if gotChannelID != channelID {
		t.Fatalf("expected channel %d, got %d", channelID, gotChannelID)
	}
}

func TestParseDeepLinkPayloadRejectsTamper(t *testing.T) {
	secret := "test-secret"
	channelID := int64(-1001234567890)
	payload := BuildDeepLinkPayload(channelID, time.Now(), secret)

	tampered := payload[:len(payload)-1] + "0"
	if _, err := ParseAndVerifyDeepLinkPayload(tampered, secret, time.Hour); err == nil {
		t.Fatal("expected tampered payload to fail signature validation")
	}
}
