package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetTextPlainString(t *testing.T) {
	textJSON, _ := json.Marshal("Hello channel post")
	msg := Message{ID: 1, Type: "message", Text: textJSON}

	text, err := msg.GetText()
	assert.NoError(t, err)
	assert.Equal(t, "Hello channel post", text)
}

func TestGetTextMixedEntities(t *testing.T) {
	raw := `[{"type":"bold","text":"Title"},{"type":"plain","text":": body text"}]`
	msg := Message{ID: 2, Type: "message", Text: json.RawMessage(raw)}

	text, err := msg.GetText()
	assert.NoError(t, err)
	assert.Equal(t, "Title: body text", text)
}

func TestGetTextEmpty(t *testing.T) {
	msg := Message{ID: 3, Type: "message"}

	text, err := msg.GetText()
	assert.NoError(t, err)
	assert.Equal(t, "", text)
}

func TestMakePointIDUnique(t *testing.T) {
	id1 := makePointID(-100123, 42)
	id2 := makePointID(-100123, 43)
	id3 := makePointID(-100124, 42)

	assert.NotEqual(t, id1, id2)
	assert.NotEqual(t, id1, id3)
	assert.NotEqual(t, id2, id3)
}
