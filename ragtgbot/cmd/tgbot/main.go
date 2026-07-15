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
	"time"
	"unicode"

	tele "gopkg.in/telebot.v3"
)

const (
	defaultEmbeddingServiceAddress = "http://localhost:8000/embeddings"
	defaultQdrantServiceAddress    = "http://localhost:6333"
	defaultRedisAddress            = "redis:6379"
	collectionName                 = "chat_history"
	searchCandidateLimit           = 20
	defaultVectorSearchLimit       = 3
	defaultSearchMinScore          = 0.40
	defaultSearchScoreGap          = 0.04
	minLetterTokenRunes            = 3
	defaultOwnerUserIDs            = "1781506158"
	deepLinkHelpMessage            = "Откройте бота по ссылке из нужного канала, чтобы привязать канал к диалогу."
	restrictedAccessMessage        = "Этот бот работает только в разрешённых чатах. Вы можете развернуть свою копию: https://github.com/korjavin/ragtgbot"
)

var (
	embeddingServiceAddress string
	qdrantServiceAddress    string
	allowedQueryChats       []int64
	allowedChannelIDs       []int64
	vectorSearchLimit       int
	requireBotMention       bool
	searchMinScore          float64
	searchScoreGap          float64
	ownerUserIDs            []int64
	channelContextStore     *ChannelContextStore
	deepLinkSecret          string
	deepLinkMaxAge          time.Duration
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

func searchQdrant(embedding []float32, limit int, minScore float64, channelIDs []int64) ([]map[string]interface{}, error) {
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
		"limit":           limit,
		"score_threshold": minScore,
		"with_payload":    true,
	}

	if len(channelIDs) > 0 {
		ids := make([]interface{}, len(channelIDs))
		for i, id := range channelIDs {
			ids[i] = id
		}
		searchRequest["filter"] = map[string]interface{}{
			"must": []map[string]interface{}{
				{
					"key": "chat_id",
					"match": map[string]interface{}{
						"any": ids,
					},
				},
			},
		}
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

func parseOwnerIDs() []int64 {
	raw := strings.TrimSpace(os.Getenv("BOT_OWNER_IDS"))
	if raw == "" {
		raw = defaultOwnerUserIDs
	}
	ids := parseInt64List(raw)
	if len(ids) == 0 {
		log.Println("Warning: BOT_OWNER_IDS is empty, fallback to default owner")
		ids = parseInt64List(defaultOwnerUserIDs)
	}
	return ids
}

func isOwnerUser(userID int64) bool {
	for _, ownerID := range ownerUserIDs {
		if ownerID == userID {
			return true
		}
	}
	return false
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

func parseDurationHoursEnv(name string, defaultHours int) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return time.Duration(defaultHours) * time.Hour
	}
	hours, err := strconv.Atoi(raw)
	if err != nil || hours <= 0 {
		log.Printf("Warning: invalid %s=%q, using default %dh", name, raw, defaultHours)
		return time.Duration(defaultHours) * time.Hour
	}
	return time.Duration(hours) * time.Hour
}

func initChannelContextStore() error {
	redisEnabled := parseBoolEnv(os.Getenv("REDIS_ENABLED"), true)
	if !redisEnabled {
		log.Println("Redis context store is disabled by REDIS_ENABLED=false")
		return nil
	}

	redisAddress := strings.TrimSpace(os.Getenv("REDIS_ADDRESS"))
	if redisAddress == "" {
		redisAddress = defaultRedisAddress
	}
	redisPassword := os.Getenv("REDIS_PASSWORD")
	redisDB := 0
	if dbRaw := strings.TrimSpace(os.Getenv("REDIS_DB")); dbRaw != "" {
		parsed, err := strconv.Atoi(dbRaw)
		if err != nil {
			return fmt.Errorf("invalid REDIS_DB: %w", err)
		}
		redisDB = parsed
	}
	contextTTL := parseDurationHoursEnv("REDIS_CONTEXT_TTL_HOURS", defaultDeepLinkTTLHours)
	deepLinkMaxAge = parseDurationHoursEnv("DEEP_LINK_MAX_AGE_HOURS", defaultDeepLinkTTLHours)

	deepLinkSecret = strings.TrimSpace(os.Getenv("DEEP_LINK_SECRET"))
	if deepLinkSecret == "" {
		return fmt.Errorf("DEEP_LINK_SECRET is required when Redis context store is enabled")
	}

	store := NewChannelContextStore(redisAddress, redisPassword, redisDB, contextTTL)
	if err := store.Ping(context.Background()); err != nil {
		return fmt.Errorf("redis ping failed: %w", err)
	}
	channelContextStore = store
	log.Printf("Redis context store enabled at %s (db=%d, ttl=%s)", redisAddress, redisDB, contextTTL)
	return nil
}

func resolveSearchChannels(c tele.Context) ([]int64, error) {
	if channelContextStore == nil {
		return allowedChannelIDs, nil
	}
	if c.Sender() == nil {
		return nil, fmt.Errorf("unknown sender")
	}
	channelID, found, err := channelContextStore.GetActiveChannelForUser(context.Background(), c.Sender().ID)
	if err != nil {
		return nil, err
	}
	if !found {
		if len(allowedChannelIDs) > 0 {
			return allowedChannelIDs, nil
		}
		return nil, nil
	}
	return []int64{channelID}, nil
}

func handleStart(c tele.Context) error {
	if c.Sender() == nil {
		return c.Send("Не удалось определить пользователя.")
	}

	payload := extractStartPayload(c)
	if payload != "" && channelContextStore != nil {
		channelID, err := ParseAndVerifyDeepLinkPayload(payload, deepLinkSecret, deepLinkMaxAge)
		if err != nil {
			log.Printf("Invalid /start payload from user %d: %v", c.Sender().ID, err)
			return c.Send("Ссылка устарела или некорректна. Получите новую ссылку в нужном канале.")
		}
		if len(allowedChannelIDs) > 0 && !isAllowedChat(channelID, allowedChannelIDs) {
			return c.Send("Этот канал не разрешён для поиска.")
		}
		contextKey, err := channelContextStore.ActivateChannelForUser(context.Background(), c.Sender().ID, channelID)
		if err != nil {
			log.Printf("Failed to activate channel context: %v", err)
			return c.Send("Не удалось сохранить контекст канала. Попробуйте позже.")
		}
		log.Printf("Activated channel %d for user %d context=%s", channelID, c.Sender().ID, contextKey)
		return c.Send(fmt.Sprintf("Канал привязан: %d. Теперь запросы будут идти только по нему.", channelID))
	}

	msg := "Отправьте текстовый запрос — я найду и перешлю подходящие посты из выбранного канала."
	if channelContextStore != nil {
		msg += "\n\nДля выбора канала перейдите по deep-link из канала."
	} else {
		msg += "\n\n" + deepLinkHelpMessage
	}
	if requireBotMention {
		msg += "\nВ группах упоминайте бота: @" + c.Bot().Me.Username + " ваш запрос"
	}
	return c.Send(msg)
}

func extractStartPayload(c tele.Context) string {
	if msg := c.Message(); msg != nil {
		if payload := strings.TrimSpace(msg.Payload); payload != "" {
			return payload
		}
	}

	args := c.Args()
	if len(args) > 0 {
		return strings.TrimSpace(args[0])
	}

	text := strings.TrimSpace(c.Text())
	if text == "" {
		return ""
	}
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return ""
	}
	command := parts[0]
	if strings.HasPrefix(command, "/start") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func handleChannelCommand(c tele.Context) error {
	if c.Sender() == nil {
		return c.Send("Не удалось определить пользователя.")
	}
	if channelContextStore == nil {
		return c.Send("Контекст канала отключён (Redis).")
	}

	channelID, found, err := channelContextStore.GetActiveChannelForUser(context.Background(), c.Sender().ID)
	if err != nil {
		log.Printf("Failed to read active channel for user %d: %v", c.Sender().ID, err)
		return c.Send("Ошибка чтения текущей привязки.")
	}
	if !found {
		return c.Send("Канал не привязан. Откройте бота по deep-link из нужного канала.")
	}
	return c.Send(fmt.Sprintf("Текущая привязка: %d", channelID))
}

func handleLinkCommand(c tele.Context, botUsername string) error {
	if c.Sender() == nil {
		return c.Send("Не удалось определить пользователя.")
	}
	if !isOwnerUser(c.Sender().ID) {
		return c.Send("Команда /link доступна только owner.")
	}

	if deepLinkSecret == "" {
		return c.Send("Deep-link отключён: отсутствует DEEP_LINK_SECRET.")
	}

	args := c.Args()
	if len(args) != 1 {
		return c.Send("Использование: /link <channel_id>. Пример: /link -1001234567890")
	}

	channelID, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
	if err != nil {
		return c.Send("Некорректный channel_id. Ожидается число вида -1001234567890.")
	}
	if len(allowedChannelIDs) > 0 && !isAllowedChat(channelID, allowedChannelIDs) {
		return c.Send("Этот канал не входит в TG_CHANNEL_LIST.")
	}

	payload := BuildDeepLinkPayload(channelID, time.Now(), deepLinkSecret)
	link := fmt.Sprintf("https://t.me/%s?start=%s", botUsername, payload)
	return c.Send("Ссылка для канала:\n" + link)
}

func extractPayloadFromText(text, botUsername string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Allow raw payload to be sent directly.
	if strings.HasPrefix(text, "v1.") {
		return text
	}

	prefixes := []string{
		"https://t.me/" + botUsername + "?start=",
		"http://t.me/" + botUsername + "?start=",
		"t.me/" + botUsername + "?start=",
	}

	lowerText := strings.ToLower(text)
	for _, prefix := range prefixes {
		lowerPrefix := strings.ToLower(prefix)
		idx := strings.Index(lowerText, lowerPrefix)
		if idx < 0 {
			continue
		}
		start := idx + len(prefix)
		rest := text[start:]
		if rest == "" {
			return ""
		}
		// Payload ends at first whitespace or URL delimiter.
		end := len(rest)
		for i, r := range rest {
			if r == ' ' || r == '\n' || r == '\t' || r == '&' {
				end = i
				break
			}
		}
		return strings.TrimSpace(rest[:end])
	}

	return ""
}

func tryActivateFromText(c tele.Context, botUsername string) (bool, error) {
	if channelContextStore == nil || c.Sender() == nil || c.Message() == nil {
		return false, nil
	}

	payload := extractPayloadFromText(c.Text(), botUsername)
	if payload == "" {
		return false, nil
	}

	channelID, err := ParseAndVerifyDeepLinkPayload(payload, deepLinkSecret, deepLinkMaxAge)
	if err != nil {
		return true, c.Send("Ссылка некорректна или устарела.")
	}
	if len(allowedChannelIDs) > 0 && !isAllowedChat(channelID, allowedChannelIDs) {
		return true, c.Send("Этот канал не входит в TG_CHANNEL_LIST.")
	}

	contextKey, err := channelContextStore.ActivateChannelForUser(context.Background(), c.Sender().ID, channelID)
	if err != nil {
		log.Printf("Failed to activate channel context from text: %v", err)
		return true, c.Send("Не удалось переключить канал. Попробуйте позже.")
	}

	log.Printf("Activated channel %d for user %d context=%s (from text)", channelID, c.Sender().ID, contextKey)
	return true, c.Send(fmt.Sprintf("Канал переключен: %d", channelID))
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

func payloadFloat(payload map[string]interface{}, key string) (float64, bool) {
	value, ok := payload[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

func resultScore(result map[string]interface{}) (float64, bool) {
	return payloadFloat(result, "score")
}

func payloadText(payload map[string]interface{}) string {
	text, _ := payload["text"].(string)
	return text
}

func normalizeQuery(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

var queryStopWords = map[string]struct{}{
	"и": {}, "в": {}, "во": {}, "на": {}, "с": {}, "со": {}, "по": {}, "к": {}, "ко": {},
	"у": {}, "о": {}, "об": {}, "от": {}, "до": {}, "из": {}, "за": {}, "для": {}, "при": {},
	"не": {}, "но": {}, "а": {}, "я": {}, "мы": {}, "вы": {}, "он": {}, "она": {}, "они": {},
	"что": {}, "как": {}, "где": {}, "когда": {}, "кто": {}, "это": {}, "этот": {}, "эта": {}, "эти": {},
	"ли": {}, "же": {}, "бы": {}, "был": {}, "была": {}, "были": {}, "есть": {},
	"улица": {}, "улицу": {}, "улице": {}, "проспект": {}, "проспекте": {}, "проспекта": {},
	"пер": {}, "переулок": {}, "переулке": {}, "дом": {}, "дома": {}, "корп": {}, "корпус": {},
}

func isStopWord(token string) bool {
	_, ok := queryStopWords[token]
	return ok
}

func isNumericToken(token string) bool {
	for _, r := range token {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return len(token) > 0
}

func tokenizeSignificant(text string) []string {
	normalized := normalizeQuery(text)
	parts := strings.FieldsFunc(normalized, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})

	tokens := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || isStopWord(part) {
			continue
		}

		runes := []rune(part)
		if unicode.IsDigit(runes[0]) {
			if len(runes) >= 1 {
				if _, ok := seen[part]; !ok {
					seen[part] = struct{}{}
					tokens = append(tokens, part)
				}
			}
			continue
		}

		if len(runes) < minLetterTokenRunes {
			continue
		}

		if _, ok := seen[part]; !ok {
			seen[part] = struct{}{}
			tokens = append(tokens, part)
		}
	}

	return tokens
}

func textHasToken(text, token string) bool {
	text = normalizeQuery(text)
	token = normalizeQuery(token)
	if token == "" {
		return false
	}

	if isNumericToken(token) {
		return strings.Contains(text, token)
	}

	if len([]rune(token)) < minLetterTokenRunes {
		return false
	}

	return strings.Contains(text, token)
}

func countMatchedTokens(text string, tokens []string) int {
	matched := 0
	for _, token := range tokens {
		if textHasToken(text, token) {
			matched++
		}
	}
	return matched
}

type rankedResult struct {
	result        map[string]interface{}
	score         float64
	text          string
	matchedTokens int
	totalTokens   int
	tokenRatio    float64
	combinedScore float64
}

func acceptsCandidate(item rankedResult, queryTokens []string) bool {
	if item.totalTokens == 0 {
		return item.score >= 0.65
	}

	if item.matchedTokens == 0 {
		return item.score >= 0.72
	}

	if len(queryTokens) == 1 {
		return item.matchedTokens == 1 && item.score >= 0.42
	}

	if item.tokenRatio >= 0.5 && item.score >= 0.42 {
		return true
	}

	if item.matchedTokens >= 2 && item.score >= 0.40 {
		return true
	}

	if item.matchedTokens >= 1 && item.score >= 0.58 {
		return true
	}

	return item.score >= 0.68 && item.matchedTokens >= 1
}

func filterSearchResults(results []map[string]interface{}, query string, limit int, minScore, scoreGap float64) []map[string]interface{} {
	queryTokens := tokenizeSignificant(query)
	log.Printf("Query tokens: %v", queryTokens)

	var items []rankedResult
	for _, result := range results {
		score, ok := resultScore(result)
		if !ok || score < minScore {
			continue
		}

		payload, ok := result["payload"].(map[string]interface{})
		if !ok {
			continue
		}

		text := payloadText(payload)
		if text == "" {
			continue
		}

		matched := countMatchedTokens(text, queryTokens)
		total := len(queryTokens)
		ratio := 0.0
		if total > 0 {
			ratio = float64(matched) / float64(total)
		}

		item := rankedResult{
			result:        result,
			score:         score,
			text:          text,
			matchedTokens: matched,
			totalTokens:   total,
			tokenRatio:    ratio,
			combinedScore: score*0.55 + ratio*0.45,
		}

		if !acceptsCandidate(item, queryTokens) {
			continue
		}

		items = append(items, item)
	}

	if len(items) == 0 {
		return nil
	}

	// Sort by combined score descending (simple bubble for small N).
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].combinedScore > items[i].combinedScore {
				items[i], items[j] = items[j], items[i]
			}
		}
	}

	bestCombined := items[0].combinedScore
	filtered := make([]rankedResult, 0, limit)
	for _, item := range items {
		if bestCombined-item.combinedScore > scoreGap {
			continue
		}
		filtered = append(filtered, item)
		if len(filtered) >= limit {
			break
		}
	}

	output := make([]map[string]interface{}, len(filtered))
	for i, item := range filtered {
		log.Printf(
			"Selected result vector=%.3f tokens=%d/%d combined=%.3f text=%q",
			item.score, item.matchedTokens, item.totalTokens, item.combinedScore, truncateForLog(item.text, 80),
		)
		output[i] = item.result
	}

	return output
}

func truncateForLog(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "..."
}

func isSearchableUserMessage(msg *tele.Message) bool {
	if msg == nil {
		return false
	}
	if msg.IsForwarded() || msg.AutomaticForward {
		return false
	}
	return true
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

func handleSearchQuery(c tele.Context, query string, channelIDs []int64) error {
	log.Printf("Search query: %q in chat %d", query, c.Chat().ID)

	embedding, err := getEmbeddings([]string{query})
	if err != nil {
		log.Printf("Embedding error: %v", err)
		return c.Send("Ошибка обработки запроса.")
	}

	results, err := searchQdrant(embedding, searchCandidateLimit, searchMinScore, channelIDs)
	if err != nil {
		log.Printf("Search error: %v", err)
		return c.Send("Ошибка поиска.")
	}

	results = filterSearchResults(results, query, vectorSearchLimit, searchMinScore, searchScoreGap)
	if len(results) == 0 {
		return c.Send("Подходящих постов не найдено.")
	}

	forwarded := 0
	seen := make(map[string]struct{})

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

		dedupeKey := fmt.Sprintf("%d:%d", chatID, messageID)
		if _, exists := seen[dedupeKey]; exists {
			continue
		}
		seen[dedupeKey] = struct{}{}

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

	searchMinScore = defaultSearchMinScore
	if scoreStr := os.Getenv("SEARCH_MIN_SCORE"); scoreStr != "" {
		if score, err := strconv.ParseFloat(scoreStr, 64); err == nil && score > 0 && score <= 1 {
			searchMinScore = score
		}
	}

	searchScoreGap = defaultSearchScoreGap
	if gapStr := os.Getenv("SEARCH_SCORE_GAP"); gapStr != "" {
		if gap, err := strconv.ParseFloat(gapStr, 64); err == nil && gap >= 0 && gap <= 1 {
			searchScoreGap = gap
		}
	}

	allowedQueryChats = parseInt64List(os.Getenv("TG_GROUP_LIST"))
	allowedChannelIDs = parseInt64List(os.Getenv("TG_CHANNEL_LIST"))
	ownerUserIDs = parseOwnerIDs()
	requireBotMention = parseBoolEnv(os.Getenv("REQUIRE_BOT_MENTION"), false)
	if err := initChannelContextStore(); err != nil {
		log.Fatalf("Failed to initialize channel context store: %v", err)
	}
	if channelContextStore != nil {
		defer func() {
			if err := channelContextStore.Close(); err != nil {
				log.Printf("Failed to close channel context store: %v", err)
			}
		}()
	}

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
	log.Printf("Search config: limit=%d min_score=%.2f score_gap=%.2f", vectorSearchLimit, searchMinScore, searchScoreGap)
	log.Printf("Owner users configured: %d", len(ownerUserIDs))

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

	b.Handle("/start", handleStart)
	b.Handle("/link", func(c tele.Context) error {
		return handleLinkCommand(c, b.Me.Username)
	})
	b.Handle("/channel", handleChannelCommand)

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

		if !isSearchableUserMessage(c.Message()) {
			return nil
		}

		activated, err := tryActivateFromText(c, b.Me.Username)
		if err != nil {
			return err
		}
		if activated {
			return nil
		}

		query, isQuery := extractQuery(c, b.Me.Username, requireBotMention)
		if !isQuery {
			return nil
		}

		if !isAllowedChat(c.Chat().ID, allowedQueryChats) {
			return c.Send(restrictedAccessMessage)
		}

		searchChannelIDs, err := resolveSearchChannels(c)
		if err != nil {
			log.Printf("Failed to resolve channel context: %v", err)
			return c.Send("Ошибка получения контекста канала. Попробуйте позже.")
		}
		if len(searchChannelIDs) == 0 {
			return c.Send("Канал для поиска не выбран. " + deepLinkHelpMessage)
		}

		return handleSearchQuery(c, query, searchChannelIDs)
	})

	go b.Start()

	log.Println("Bot is running. Press Ctrl+C to stop.")
	<-ctx.Done()

	log.Println("Stopping bot...")
	b.Stop()
}
