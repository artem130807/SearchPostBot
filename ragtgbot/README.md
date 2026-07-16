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
| `redis` | Контекст пользователя (`user_id -> active channel`) |
| `cmd/uploadbackup` | Импорт истории канала из JSON-экспорта Telegram Desktop |

## Переменные окружения

**Обязательные:**
- `TELEGRAM_BOT_TOKEN_1..5` — токены 5 ботов от BotFather (для `docker-compose`)

**Опциональные:**
- `TG_CHANNEL_LIST` — ID каналов для индексации через запятую (если пусто — все каналы, где бот админ)
- `TG_GROUP_LIST` — ID чатов, где принимаются запросы (если пусто — везде)
- `VECTOR_SEARCH_LIMIT` — сколько постов пересылать (по умолчанию 3)
- `EMBEDDING_SERVICE_ADDRESS` — URL сервиса embeddings
- `QDRANT_SERVICE_ADDRESS` — URL Qdrant
- `DEEP_LINK_SECRET` — общий секрет для подписанных deep-link payload
- `REDIS_ADDRESS` / `REDIS_DB` / `REDIS_PASSWORD` — подключение к Redis
- `REDIS_KEY_PREFIX_1..5` — namespace ключей Redis для каждого бота (чтобы контексты не пересекались)
- `BOT_OWNER_IDS` — список Telegram user ID владельцев (через запятую), кто может вызывать `/link` (default: `1781506158`)

## Запуск

```bash
export TELEGRAM_BOT_TOKEN=your_token
export TG_CHANNEL_LIST=-1001234567890
docker-compose up -d
```

Для запуска пяти ботов заполните `.env` токенами `TELEGRAM_BOT_TOKEN_1..5`.

## Настройка Telegram

1. Создайте бота через [@BotFather](https://t.me/BotFather).
2. Добавьте бота **администратором** в канал (нужно право публикации/чтения постов).
3. Узнайте ID канала (например, через [@userinfobot](https://t.me/userinfobot) или логи бота) и укажите в `TG_CHANNEL_LIST`.
4. Owner генерирует deep-link: `/link -1001234567890` (в личке с ботом).
5. Разместите ссылку в нужном канале.
6. Пользователь открывает бота по ссылке, после чего его запросы идут строго по этому каналу.

Пример deep-link:
`https://t.me/<bot_username>?start=v1.<channel_id>.<ts>.<signature>`

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
