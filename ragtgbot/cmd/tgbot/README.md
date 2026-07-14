# Telegram Channel Search Bot

Бот индексирует посты Telegram-канала и пересылает пользователю сообщения, подходящие по смыслу к запросу.

## Поведение

- **Канал** → каждый пост индексируется в Qdrant (1 пост = 1 запись)
- **Запрос** → в личке или `@bot запрос` в группе
- **Ответ** → forward найденных постов (без OpenAI)

## Запуск

```bash
export TELEGRAM_BOT_TOKEN=...
export TG_CHANNEL_LIST=-1001234567890
go run main.go
```

## Переменные

| Переменная | Описание |
|------------|----------|
| `TELEGRAM_BOT_TOKEN` | Токен бота (обязательно) |
| `TG_CHANNEL_LIST` | ID каналов для индексации |
| `TG_GROUP_LIST` | ID чатов для приёма запросов |
| `VECTOR_SEARCH_LIMIT` | Кол-во пересылаемых постов (default: 5) |
| `EMBEDDING_SERVICE_ADDRESS` | default: `http://localhost:8000/embeddings` |
| `QDRANT_SERVICE_ADDRESS` | default: `http://localhost:6333` |
