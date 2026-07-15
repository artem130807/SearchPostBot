package main

import "testing"

func TestParseOwnerIDsDefault(t *testing.T) {
	t.Setenv("BOT_OWNER_IDS", "")
	ids := parseOwnerIDs()
	if len(ids) != 1 || ids[0] != 1781506158 {
		t.Fatalf("unexpected default owner ids: %v", ids)
	}
}

func TestParseOwnerIDsFromEnv(t *testing.T) {
	t.Setenv("BOT_OWNER_IDS", "1,2,3")
	ids := parseOwnerIDs()
	if len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 3 {
		t.Fatalf("unexpected owner ids from env: %v", ids)
	}
}
