package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	tele "gopkg.in/telebot.v3"
)

const (
	defaultEmbeddingServiceAddress = "http://localhost:8000/embeddings"
	defaultQdrantServiceAddress    = "http://localhost:6333"
	collectionName                 = "chat_history"
	defaultVectorSearchLimit       = 5
	restrictedAccessMessage        = "Этот бот работает только в разрешённых чатах. Вы можете развернуть свою копию: https://github.com/korjavin/ragtgbot"
)

var (
	embeddingServiceAddress string
	qdrantServiceAddress    string
	allowedQueryChats       []int64
	allowedChannelIDs       []int64
	vectorSearchLimit       int
	requireBotMention       bool
)

type TextList struct {
	Texts []string `json:"texts"`
}

func getEmbeddings(texts []string) ([]float32, error) {
	jsonData, err := json.Marshal(TextList{Texts: texts})
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(embeddingServiceAddress, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var embeddingString string
	if err := json.Unmarshal(body, &embeddingString); err != nil {
		return nil, err
	}

	var embeddingList [][]float32
	if err := json.Unmarshal([]byte(embeddingString), &embeddingList); err != nil {
		return nil, err
	}
	if len(embeddingList) == 0 {
		return nil, fmt.Errorf("no embeddings returned from service")
	}

	return embeddingList[0], nil
}

func makePointID(chatID int64, messageID int) int64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d:%d", chatID, messageID)
	return int64(h.Sum64())
}

func saveToQdrant(pointID int64, chatID int64, messageID int, text string, embedding []float32) error {
	qdrantURL := fmt.Sprintf("%s/collections/%s/points", qdrantServiceAddress, collectionName)

	embeddingInterface := make([]interface{}, len(embedding))
	for i, v := range embedding {
		embeddingInterface[i] = v
	}

	point := map[string]interface{}{
		"id": pointID,
		"vector": map[string]interface{}{
			"data": embeddingInterface,
		},
		"payload": map[string]interface{}{
			"text":       text,
			"chat_id":    chatID,
			"message_id": messageID,
		},
	}

	requestBody, err := json.Marshal(map[string][]map[string]interface{}{
		"points": {point},
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPut, qdrantURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("error response from Qdrant: %s", string(respBody))
	}

	log.Printf("Indexed post chat=%d message=%d point=%d", chatID, messageID, pointID)
	return nil
}

func searchQdrant(embedding []float32, limit int) ([]map[string]interface{}, error) {
	qdrantURL := fmt.Sprintf("%s/collections/%s/points/search", qdrantServiceAddress, collectionName)

	embeddingInterface := make([]interface{}, len(embedding))
	for i, v := range embedding {
		embeddingInterface[i] = v
	}

	searchRequest := map[string]interface{}{
		"vector": map[string]interface{}{
			"name":   "data",
			"vector": embeddingInterface,
		},
		"limit":        limit,
		"with_payload": true,
	}

	requestBody, err := json.Marshal(searchRequest)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, qdrantURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("error response from Qdrant: %s", string(respBody))
	}

	var searchResult map[string]interface{}
	if err := json.Unmarshal(respBody, &searchResult); err != nil {
		return nil, err
	}

	resultArray, ok := searchResult["result"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("result field is not an array")
	}

	results := make([]map[string]interface{}, 0, len(resultArray))
	for _, r := range resultArray {
		result, ok := r.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("result item is not a map")
		}
		results = append(results, result)
	}

	return results, nil
}

func deleteQdrantCollection(name string) error {
	qdrantURL := fmt.Sprintf("%s/collections/%s", qdrantServiceAddress, name)
	req, err := http.NewRequest(http.MethodDelete, qdrantURL, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("error response from Qdrant: %s", string(respBody))
	}
	return nil
}

func createQdrantCollection(name string) error {
	qdrantURL := fmt.Sprintf("%s/collections/%s", qdrantServiceAddress, name)
	resp, err := http.Get(qdrantURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		var collectionInfo map[string]interface{}
		if err := json.Unmarshal(body, &collectionInfo); err != nil {
			return err
		}

		result, ok := collectionInfo["result"].(map[string]interface{})
		if ok {
			config, ok := result["config"].(map[string]interface{})
			if ok {
				params, ok := config["params"].(map[string]interface{})
				if ok {
					vectors, ok := params["vectors"].(map[string]interface{})
					if ok {
						if _, hasDataVector := vectors["data"]; hasDataVector {
							return nil
						}
					}
				}
			}
		}

		if err := deleteQdrantCollection(name); err != nil {
			return err
		}
	}

	requestBody, err := json.Marshal(map[string]interface{}{
		"vectors": map[string]interface{}{
			"data": map[string]interface{}{
				"size":     512,
				"distance": "Cosine",
			},
		},
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPut, qdrantURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("error response from Qdrant: %s", string(respBody))
	}

	return nil
}

func isAllowedChat(chatID int64, allowed []int64) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, id := range allowed {
		if chatID == id {
			return true
		}
	}
	return false
}

func parseInt64List(envValue string) []int64 {
	if envValue == "" {
		return nil
	}

	var result []int64
	for _, part := range strings.Split(envValue, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			log.Printf("Warning: invalid ID in list: %s", part)
			continue
		}
		result = append(result, id)
	}
	return result
}

func messageText(msg *tele.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Text != "" {
		return msg.Text
	}
	return msg.Caption
}

func parseBoolEnv(value string, defaultValue bool) bool {
	if value == "" {
		return defaultValue
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultValue
	}
}

func extractQuery(c tele.Context, botUsername string, requireMention bool) (string, bool) {
	text := strings.TrimSpace(c.Text())
	if text == "" {
		return "", false
	}

	if strings.HasPrefix(text, "/") {
		return "", false
	}

	if c.Chat().Type == tele.ChatPrivate {
		return text, true
	}

	if !requireMention {
		return text, true
	}

	mention := "@" + botUsername
	if !strings.Contains(text, mention) {
		return "", false
	}

	query := strings.TrimSpace(strings.ReplaceAll(text, mention, ""))
	return query, query != ""
}

func payloadInt64(payload map[string]interface{}, key string) (int64, bool) {
	value, ok := payload[key]
	if !ok {
		return 0, false
	}

	switch v := value.(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}

func payloadInt(payload map[string]interface{}, key string) (int, bool) {
	value, ok := payloadInt64(payload, key)
	if !ok {
		return 0, false
	}
	return int(value), true
}

func indexChannelPost(c tele.Context) error {
	chatID := c.Chat().ID
	if !isAllowedChat(chatID, allowedChannelIDs) {
		log.Printf("Channel %d is not in TG_CHANNEL_LIST, skipping", chatID)
		return nil
	}

	msg := c.Message()
	text := messageText(msg)
	if text == "" {
		log.Printf("Skipping channel post %d without text/caption", msg.ID)
		return nil
	}

	embedding, err := getEmbeddings([]string{text})
	if err != nil {
		return fmt.Errorf("embedding error: %w", err)
	}

	pointID := makePointID(chatID, msg.ID)
	return saveToQdrant(pointID, chatID, msg.ID, text, embedding)
}

func handleSearchQuery(c tele.Context, query string) error {
	log.Printf("Search query: %q in chat %d", query, c.Chat().ID)

	embedding, err := getEmbeddings([]string{query})
	if err != nil {
		log.Printf("Embedding error: %v", err)
		return c.Send("Ошибка обработки запроса.")
	}

	results, err := searchQdrant(embedding, vectorSearchLimit)
	if err != nil {
		log.Printf("Search error: %v", err)
		return c.Send("Ошибка поиска.")
	}

	forwarded := 0
	for _, result := range results {
		payload, ok := result["payload"].(map[string]interface{})
		if !ok {
			continue
		}

		chatID, ok := payloadInt64(payload, "chat_id")
		if !ok {
			continue
		}
		messageID, ok := payloadInt(payload, "message_id")
		if !ok {
			continue
		}

		src := &tele.Message{
			ID:   messageID,
			Chat: &tele.Chat{ID: chatID},
		}
		if err := c.Forward(src); err != nil {
			log.Printf("Forward failed chat=%d message=%d: %v", chatID, messageID, err)
			continue
		}
		forwarded++
	}

	if forwarded == 0 {
		return c.Send("Подходящих постов не найдено.")
	}

	return nil
}

func main() {
	log.Println("Starting Telegram channel search bot...")

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable is required")
	}

	embeddingServiceAddress = os.Getenv("EMBEDDING_SERVICE_ADDRESS")
	if embeddingServiceAddress == "" {
		embeddingServiceAddress = defaultEmbeddingServiceAddress
	}

	qdrantServiceAddress = os.Getenv("QDRANT_SERVICE_ADDRESS")
	if qdrantServiceAddress == "" {
		qdrantServiceAddress = defaultQdrantServiceAddress
	}

	vectorSearchLimit = defaultVectorSearchLimit
	if limitStr := os.Getenv("VECTOR_SEARCH_LIMIT"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			vectorSearchLimit = limit
		}
	}

	allowedQueryChats = parseInt64List(os.Getenv("TG_GROUP_LIST"))
	allowedChannelIDs = parseInt64List(os.Getenv("TG_CHANNEL_LIST"))
	requireBotMention = parseBoolEnv(os.Getenv("REQUIRE_BOT_MENTION"), false)

	if len(allowedQueryChats) > 0 {
		log.Printf("Queries allowed in %d chat(s)", len(allowedQueryChats))
	} else {
		log.Println("Queries allowed in all chats")
	}

	if requireBotMention {
		log.Println("Bot mention is required in groups")
	} else {
		log.Println("Bot mention is not required: every text message is treated as a query")
	}

	if len(allowedChannelIDs) > 0 {
		log.Printf("Indexing enabled for %d channel(s)", len(allowedChannelIDs))
	} else {
		log.Println("Indexing enabled for all channels where bot is admin")
	}

	if err := createQdrantCollection(collectionName); err != nil {
		log.Fatalf("Failed to create/check Qdrant collection: %v", err)
	}

	pref := tele.Settings{
		Token:  token,
		Poller: &tele.LongPoller{Timeout: 10},
	}

	b, err := tele.NewBot(pref)
	if err != nil {
		log.Fatalf("Failed to create Telegram bot: %v", err)
	}
	log.Printf("Bot started: @%s", b.Me.Username)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	b.Handle("/start", func(c tele.Context) error {
		msg := "Отправьте текстовый запрос — я найду и перешлю подходящие посты из проиндексированного канала."
		if requireBotMention {
			msg += "\n\nВ группах упоминайте бота: @" + b.Me.Username + " ваш запрос"
		} else {
			msg += "\n\nВ группах можно писать запрос без упоминания бота."
		}
		return c.Send(msg)
	})

	b.Handle(tele.OnChannelPost, func(c tele.Context) error {
		if err := indexChannelPost(c); err != nil {
			log.Printf("Failed to index channel post: %v", err)
		}
		return nil
	})

	b.Handle(tele.OnEditedChannelPost, func(c tele.Context) error {
		if err := indexChannelPost(c); err != nil {
			log.Printf("Failed to re-index edited channel post: %v", err)
		}
		return nil
	})

	b.Handle(tele.OnText, func(c tele.Context) error {
		if c.Sender() != nil && c.Sender().IsBot {
			return nil
		}

		query, isQuery := extractQuery(c, b.Me.Username, requireBotMention)
		if !isQuery {
			return nil
		}

		if !isAllowedChat(c.Chat().ID, allowedQueryChats) {
			return c.Send(restrictedAccessMessage)
		}

		return handleSearchQuery(c, query)
	})

	go b.Start()

	log.Println("Bot is running. Press Ctrl+C to stop.")
	<-ctx.Done()

	log.Println("Stopping bot...")
	b.Stop()
}
