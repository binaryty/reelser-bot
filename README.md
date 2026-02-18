# Reelser Bot - Telegram Bot для скачивания видео

Telegram-бот для скачивания видео с YouTube, TikTok и Instagram (Reels и обычные видео).

## 🚀 Возможности

- 📥 Скачивание видео с **YouTube** в максимальном качестве
- 📥 Скачивание видео с **TikTok**
- 📥 Скачивание видео с **Instagram** (Reels и обычные видео)
- 🎥 Автоматическое определение платформы по ссылке
- 📤 Отправка видео в Telegram как native video file
- ⚡ Загрузка в максимальном доступном качестве
- 🧵 Параллельная обработка нескольких загрузок
- 💬 Поддержка inline-режима (`@bot ссылка` прямо в любом чате)
- 🧹 Автоматическая очистка временных файлов
- 🔒 Опциональная авторизация по токену

## 📋 Требования

- Go 1.22 или выше
- [yt-dlp](https://github.com/yt-dlp/yt-dlp) (для YouTube и Instagram)
- Telegram Bot Token (получить у [@BotFather](https://t.me/BotFather))
- Docker (опционально, если запускаете в контейнере)

## 🛠 Установка

### 1. Клонирование репозитория

```bash
git clone <repository-url>
cd Reelser-bot
```

### 2. Установка зависимостей

```bash
make deps
# или
go mod download
```

### 3. Установка yt-dlp

**Windows:**
```bash
# Через pip
pip install yt-dlp

# Или скачать с https://github.com/yt-dlp/yt-dlp/releases
```

**Linux/macOS:**
```bash
# Через pip
pip install yt-dlp

# Или через пакетный менеджер
# Ubuntu/Debian
sudo apt install yt-dlp

# macOS
brew install yt-dlp
```

### 4. Настройка конфигурации

Создайте файл `.env` на основе `env.example`:

```bash
cp env.example .env
```

Отредактируйте `.env` и укажите ваш Telegram Bot Token:

```env
TELEGRAM_BOT_TOKEN=your_telegram_bot_token_here
TEMP_DIR=./tmp
MAX_VIDEO_SIZE_MB=50
VIDEO_QUALITY=best
WORKER_POOL_SIZE=4
LOG_LEVEL=info
AUTH_ENABLED=false
AUTH_TOKENS=
```

## 🏃 Запуск

### Разработка

```bash
make run
# или
go run cmd/bot/main.go
```

### Сборка

```bash
make build
```

Запуск собранного бинарника:

```bash
./bin/Reelser-bot
# или на Windows
.\bin\Reelser-bot.exe
```

## 📖 Использование

1. Найдите бота в Telegram по его username и нажмите **Start**
2. Отправьте ссылку на видео в личные сообщения **или** используйте inline-режим:
   - В любом чате наберите `@<username_бота> <ссылка>` и выберите вариант «Скачать видео». Бот отправит результат вам в личные сообщения.
3. Поддерживаемые ссылки:
   - YouTube: `https://www.youtube.com/watch?v=...` или `https://youtu.be/...`
   - TikTok: `https://www.tiktok.com/@user/video/...`
   - Instagram: `https://www.instagram.com/reel/...` или `https://www.instagram.com/p/...`

Бот автоматически определит платформу, скачает видео в максимальном качестве и отправит его вам.

### Авторизация (опционально)

Если включена авторизация (`AUTH_ENABLED=true`):
- Пользователи должны отправить токен доступа перед использованием бота
- Токены задаются через переменную `AUTH_TOKENS` (через запятую)
- Администратор может управлять списком разрешённых пользователей через файл `AUTH_ALLOWED_USERS_FILE`

## 🐳 Запуск в Docker

1. Скопируйте настройки:
   ```bash
   cp env.example .env
   ```

2. Соберите образ:
   ```bash
   make docker-build
   ```

3. Запустите контейнер, передав переменные окружения и, при необходимости, пробросив временную директорию:
   ```bash
   make docker-run
   ```

   На Windows PowerShell при прямом запуске через `docker run` используйте `-v ${PWD}/tmp:/app/tmp`. В make-цели путь формируется автоматически.

### Docker Compose

Для долгоживущих деплоев удобнее использовать `docker-compose.yml` из репозитория:

```bash
make compose-up     # собрать образ (если нужно) и запустить сервис
make compose-logs   # посмотреть логи
make compose-down   # остановить сервис
```

Контейнер уже содержит `yt-dlp`, поэтому дополнительных зависимостей не требуется. Все переменные окружения из `.env` передаются внутрь через `--env-file`.

## 🏗 Архитектура проекта

Проект следует принципам чистой архитектуры:

```
Reelser-bot/
├── cmd/
│   └── bot/
│       └── main.go              # Точка входа приложения
├── internal/
│   ├── common/                  # Общие константы
│   │   └── constants.go
│   ├── config/                  # Конфигурация приложения
│   │   └── config.go
│   ├── transport/
│   │   └── telegram/            # Telegram транспорт
│   │       ├── bot.go
│   │       ├── handler.go
│   │       └── constants.go
│   ├── services/
│   │   └── downloader/          # Сервис загрузки видео
│   │       └── service.go
│   └── platform/                # Платформенные загрузчики
│       ├── yt/                  # YouTube
│       │   └── downloader.go
│       ├── tiktok/              # TikTok
│       │   └── downloader.go
│       └── instagram/           # Instagram
│           └── downloader.go
├── .github/
│   └── workflows/
│       └── ci.yaml              # CI/CD пайплайн
├── env.example                  # Пример конфигурации
├── Makefile                     # Команды для сборки
├── go.mod                       # Go модули
└── README.md                    # Документация
```

## 🔧 Конфигурация

### Переменные окружения

| Переменная | Описание | По умолчанию |
|------------|----------|--------------|
| `TELEGRAM_BOT_TOKEN` | Токен Telegram бота (обязательно) | - |
| `TEMP_DIR` | Директория для временных файлов | `./tmp` |
| `MAX_VIDEO_SIZE_MB` | Максимальный размер видео в MB | `50` |
| `VIDEO_QUALITY` | Качество видео (`best` или `worst`) | `best` |
| `WORKER_POOL_SIZE` | Количество параллельных загрузок | `кол-во ядер` |
| `LOG_LEVEL` | Уровень логирования (`debug`, `info`, `warn`, `error`) | `info` |
| `AUTH_ENABLED` | Включить авторизацию по токену | `false` |
| `AUTH_TOKENS` | Список токенов доступа (через запятую) | - |
| `AUTH_ALLOWED_USERS_FILE` | Файл со списком разрешённых пользователей | `./allowed_users.txt` |

## 🧪 Тестирование

```bash
make test
```

## 🔍 Линтинг

```bash
make lint
```

Для установки golangci-lint:

```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

### CI/CD

Проект использует GitHub Actions для автоматической проверки кода:
- Линтинг с golangci-lint
- Проверка форматирования gofmt
- Сборка на Linux, Windows и macOS
- Тесты на Go 1.22 и 1.23

## 📝 Команды Makefile

- `make run` - Запустить приложение
- `make build` - Собрать приложение
- `make deps` - Установить зависимости
- `make lint` - Запустить линтер
- `make test` - Запустить тесты
- `make clean` - Очистить артефакты сборки
- `make help` - Показать справку
- `make docker-build` - Собрать Docker образ
- `make docker-run` - Запустить контейнер локально
- `make compose-up` / `make compose-down` - Управлять docker-compose

## ⚠️ Ограничения

- Telegram ограничивает размер отправляемых файлов до **50 MB**
- Для больших видео бот уведомит пользователя об ошибке
- TikTok загрузка использует внешний API (TikWM), который может иметь ограничения
- Для некоторых приватных постов Instagram может потребоваться авторизация

## 🐛 Решение проблем

### Ошибка "yt-dlp not found"

Убедитесь, что `yt-dlp` установлен и доступен в PATH:

```bash
yt-dlp --version
```

### Ошибка "TELEGRAM_BOT_TOKEN is required"

Проверьте, что файл `.env` существует и содержит правильный токен бота.

### Видео не скачивается

- Проверьте логи приложения
- Убедитесь, что ссылка валидна
- Проверьте подключение к интернету
- Для Instagram может потребоваться авторизация в yt-dlp

### Ошибка авторизации

Если включена авторизация (`AUTH_ENABLED=true`), пользователи должны:
1. Отправить `/start` боту
2. Ввести токен доступа, полученный от администратора
3. После успешной авторизации можно отправлять ссылки на видео

## 🔄 История изменений

### Последние изменения

- ✅ Упрощена загрузка YouTube видео (автоматический выбор максимального качества)
- ✅ Убран выбор качества видео (теперь всегда "best")
- ✅ Проведён рефакторинг снижения когнитивной сложности кода
- ✅ Добавлена опциональная авторизация по токену
- ✅ Улучшена обработка ошибок и логирование
- ✅ Добавлен CI/CD пайплайн

## 📄 Лицензия

MIT

## 🤝 Вклад

Pull requests приветствуются! Для больших изменений сначала откройте issue для обсуждения.

## 📧 Контакты

Если у вас есть вопросы или предложения, создайте issue в репозитории.
