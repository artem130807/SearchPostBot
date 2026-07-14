# Telegram Backup Uploader

Импортирует JSON-экспорт Telegram (канал или группу) в Qdrant: **одно сообщение = одна точка** с `chat_id`, `message_id` и embedding.

## Usage

1. Экспортируйте канал через Telegram Desktop (JSON).
2. Запустите Qdrant и embedding service (`docker-compose up qdrant embedding_service`).
3. Импортируйте:

```bash
go run cmd/uploadbackup/main.go path/to/export.json
```

## Переменные

- `QDRANT_SERVICE_ADDRESS` — default: `http://localhost:6333`
- `EMBEDDING_SERVICE_ADDRESS` — default: `http://localhost:8000/embeddings`
