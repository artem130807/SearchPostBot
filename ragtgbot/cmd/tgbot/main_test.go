package main

import "testing"

func TestTokenizeSignificant(t *testing.T) {
	tokens := tokenizeSignificant("улицу Авиастроителей 15")
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %v", tokens)
	}
	if tokens[0] != "авиастроителей" || tokens[1] != "15" {
		t.Fatalf("unexpected tokens: %v", tokens)
	}
}

func TestCountMatchedTokensAddressQuery(t *testing.T) {
	post := "Понравилась девушка, видел на проспекте Авиастроителей 15"
	tokens := tokenizeSignificant("улицу Авиастроителей 15")

	matched := countMatchedTokens(post, tokens)
	if matched != 2 {
		t.Fatalf("expected 2 matched tokens, got %d", matched)
	}
}

func TestSingleNameQueryDoesNotMatchShortPost(t *testing.T) {
	tokens := tokenizeSignificant("Артём")
	matched := countMatchedTokens("А", tokens)
	if matched != 0 {
		t.Fatalf("expected 0 matches for post %q, got %d", "А", matched)
	}
}

func TestSingleNameQueryMatchesFullPost(t *testing.T) {
	tokens := tokenizeSignificant("Артём")
	matched := countMatchedTokens("Артём 123 аб во", tokens)
	if matched != 1 {
		t.Fatalf("expected 1 match, got %d", matched)
	}
}
