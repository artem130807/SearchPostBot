# Telegram Channel Search Bot

Бот индексирует посты Telegram-канала и пересылает пользователю сообщения, подходящие по смыслу к запросу.

## Поведение

- **Канал** → каждый пост индексируется в Qdrant (1 пост = 1 запись)
- **Контекст пользователя** → хранится в Redis (выбранный канал после deep-link)
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
| `VECTOR_SEARCH_LIMIT` | Кол-во пересылаемых постов (default: 3) |
| `REDIS_ADDRESS` | адрес Redis, default: `redis:6379` |
| `DEEP_LINK_SECRET` | секрет для подписи deep-link payload |
| `BOT_OWNER_IDS` | Telegram user ID владельцев для команды `/link` (default: `1781506158`) |
| `EMBEDDING_SERVICE_ADDRESS` | default: `http://localhost:8000/embeddings` |
| `QDRANT_SERVICE_ADDRESS` | default: `http://localhost:6333` |

## Deep-link

1. Owner в личке с ботом отправляет `/link -1001234567890`.
2. Разместите полученную ссылку в канале.
3. Пользователь открывает бота по ссылке — канал привязывается к его `user_id` в Redis.
