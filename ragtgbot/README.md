# SearchPostBot (на базе ragtgbot)

Telegram-бот для семантического поиска постов в канале и пересылки подходящих сообщений пользователю.

## Как работает

1. **Индексация** — бот (как админ канала) получает каждый пост, считает embedding и сохраняет в Qdrant вместе с `chat_id` и `message_id`.
2. **Поиск** — пользователь отправляет текстовый запрос (в личку или с упоминанием `@bot` в группе).
3. **Ответ** — бот находит top-N похожих постов и **пересылает** их оригиналом (forward).

OpenAI не используется.

## Компоненты

| Сервис | Назначение |
|--------|------------|
| `cmd/tgbot` | Telegram-бот: индексация канала + поиск + forward |
| `embedding_service` | Векторные embeddings (multilingual) |
| `cmd/uploadbackup` | Импорт истории канала из JSON-экспорта Telegram Desktop |

## Переменные окружения

**Обязательные:**
- `TELEGRAM_BOT_TOKEN` — токен от BotFather

**Опциональные:**
- `TG_CHANNEL_LIST` — ID каналов для индексации через запятую (если пусто — все каналы, где бот админ)
- `TG_GROUP_LIST` — ID чатов, где принимаются запросы (если пусто — везде)
- `VECTOR_SEARCH_LIMIT` — сколько постов пересылать (по умолчанию 5)
- `EMBEDDING_SERVICE_ADDRESS` — URL сервиса embeddings
- `QDRANT_SERVICE_ADDRESS` — URL Qdrant

## Запуск

```bash
export TELEGRAM_BOT_TOKEN=your_token
export TG_CHANNEL_LIST=-1001234567890
docker-compose up -d
```

## Настройка Telegram

1. Создайте бота через [@BotFather](https://t.me/BotFather).
2. Добавьте бота **администратором** в канал (нужно право публикации/чтения постов).
3. Узнайте ID канала (например, через [@userinfobot](https://t.me/userinfobot) или логи бота) и укажите в `TG_CHANNEL_LIST`.
4. Напишите боту в личку или упомяните в группе: `@your_bot текст запроса`.

## Импорт истории канала

Если нужны старые посты (до добавления бота):

1. Экспортируйте канал через Telegram Desktop (JSON).
2. Запустите Qdrant и embedding service.
3. Выполните:

```bash
go run cmd/uploadbackup/main.go path/to/export.json
```

Каждое сообщение индексируется отдельно с сохранением `chat_id` и `message_id` для forward.

**Важно:** forward работает только для постов, которые бот уже «видел» (получил update или которые были импортированы, пока бот имеет доступ к каналу).
