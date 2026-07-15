package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"os"

	"github.com/cheggaaa/pb/v3"
)

var qdrantBaseURL string

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: go run cmd/uploadbackup/main.go <telegram-export.json>")
		return
	}
	filename := os.Args[1]

	qdrantAddr := os.Getenv("QDRANT_SERVICE_ADDRESS")
	if qdrantAddr != "" {
		qdrantBaseURL = qdrantAddr
	} else {
		qdrantBaseURL = "http://localhost:6333"
	}

	jsonFile, err := os.Open(filename)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer jsonFile.Close()

	byteValue, err := io.ReadAll(jsonFile)
	if err != nil {
		fmt.Println(err)
		return
	}

	var backup TelegramBackup
	if err := json.Unmarshal(byteValue, &backup); err != nil {
		fmt.Printf("Error unmarshaling JSON: %v\n", err)
		return
	}

	if err := createQdrantCollection("chat_history"); err != nil {
		fmt.Printf("Warning: collection setup failed: %v\n", err)
	}

	bar := pb.StartNew(len(backup.Messages))
	defer bar.Finish()

	indexed := 0
	skipped := 0

	for _, message := range backup.Messages {
		if message.Type != "message" {
			bar.Increment()
			continue
		}

		text, err := message.GetText()
		if err != nil || text == "" {
			skipped++
			bar.Increment()
			continue
		}

		if err := indexMessage(backup.ID, message.ID, text); err != nil {
			fmt.Printf("Error indexing message %d: %v\n", message.ID, err)
		} else {
			indexed++
		}

		bar.Increment()
	}

	fmt.Printf("Finished. Indexed %d messages, skipped %d.\n", indexed, skipped)
}

func makePointID(chatID int64, messageID int64) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d:%d", chatID, messageID)
	return h.Sum64()
}

func indexMessage(chatID int64, messageID int64, text string) error {
	embedding, err := getEmbedding(text)
	if err != nil {
		return fmt.Errorf("embedding: %w", err)
	}

	pointID := makePointID(chatID, messageID)
	return saveToQdrant(pointID, chatID, int(messageID), text, embedding)
}

func getEmbedding(text string) ([]float64, error) {
	embeddingServiceURL := os.Getenv("EMBEDDING_SERVICE_ADDRESS")
	if embeddingServiceURL == "" {
		embeddingServiceURL = "http://localhost:8000/embeddings"
	}

	requestBody, err := json.Marshal(map[string][]string{
		"texts": {text},
	})
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(embeddingServiceURL, "application/json", bytes.NewBuffer(requestBody))
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

	var embeddingList [][]float64
	if err := json.Unmarshal([]byte(embeddingString), &embeddingList); err != nil {
		return nil, err
	}
	if len(embeddingList) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	return embeddingList[0], nil
}

func saveToQdrant(pointID uint64, chatID int64, messageID int, text string, embedding []float64) error {
	qdrantURL := fmt.Sprintf("%s/collections/chat_history/points", qdrantBaseURL)

	point := map[string]interface{}{
		"id": pointID,
		"vector": map[string]interface{}{
			"data": embedding,
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

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant error: %s", string(body))
	}

	return nil
}

func createQdrantCollection(collectionName string) error {
	qdrantURL := fmt.Sprintf("%s/collections/%s", qdrantBaseURL, collectionName)

	resp, err := http.Get(qdrantURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
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

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qdrant error: %s", string(body))
	}

	return nil
}
