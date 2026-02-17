package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/reelser-bot/internal/services/auth"
	"github.com/reelser-bot/internal/services/downloader"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Handler обрабатывает входящие сообщения от Telegram
type Handler struct {
	bot            *tgbotapi.BotAPI
	botUsername    string
	logger         *slog.Logger
	downloader     *downloader.Service
	auth           *auth.Service
	stateManager   *StateManager
	downloadQueue  chan *downloadRequest
	workerCount    int
	queueSizeLimit int
}

type downloadRequest struct {
	ctx             context.Context
	cancel          context.CancelFunc
	chatID          int64
	url             string
	statusMessageID int
	source          string
	originalMessage int
}

// NewHandler создает новый обработчик Telegram
func NewHandler(
	bot *tgbotapi.BotAPI,
	botUsername string,
	logger *slog.Logger,
	downloader *downloader.Service,
	authService *auth.Service,
	maxVideoSizeMB int,
	workerCount int,
) *Handler {
	if workerCount <= 0 {
		workerCount = 1
	}

	queueSize := workerCount * 2
	handler := &Handler{
		bot:            bot,
		botUsername:    botUsername,
		logger:         logger,
		downloader:     downloader,
		auth:           authService,
		stateManager:   NewStateManager(),
		workerCount:    workerCount,
		queueSizeLimit: queueSize,
		downloadQueue:  make(chan *downloadRequest, queueSize),
	}

	handler.startWorkers()

	return handler
}

func (h *Handler) startWorkers() {
	for i := 0; i < h.workerCount; i++ {
		workerID := i + 1
		go func(id int) {
			// Обработка паник в воркерах
			defer func() {
				if r := recover(); r != nil {
					h.logger.Error("Panic recovered in download worker",
						slog.Int("worker_id", id),
						slog.Any("panic", r),
					)
				}
			}()

			h.logger.Info("Download worker started", slog.Int("worker_id", id))
			for req := range h.downloadQueue {
				h.processDownload(req)
			}
		}(workerID)
	}
}

// HandleUpdate обрабатывает обновление от Telegram
func (h *Handler) HandleUpdate(ctx context.Context, update *tgbotapi.Update) {
	// Обработка паник для предотвращения падения приложения
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("Panic recovered in HandleUpdate",
				slog.Any("panic", r),
			)
		}
	}()

	switch {
	case update.Message != nil:
		h.handleMessage(ctx, update.Message)
	case update.CallbackQuery != nil:
		h.handleCallbackQuery(ctx, update.CallbackQuery)
	case update.InlineQuery != nil:
		h.handleInlineQuery(update.InlineQuery)
	case update.ChosenInlineResult != nil:
		h.handleChosenInlineResult(ctx, update.ChosenInlineResult)
	default:
		// Игнорируем остальные типы обновлений
	}
}

func (h *Handler) handleMessage(ctx context.Context, message *tgbotapi.Message) {
	// Проверка на nil для критических полей
	if message == nil {
		h.logger.Warn("Received nil message")
		return
	}

	if message.From == nil {
		h.logger.Warn("Received message without From field", slog.Int64("chat_id", message.Chat.ID))
		return
	}

	if message.Chat == nil {
		h.logger.Warn("Received message without Chat field")
		return
	}

	chatID := message.Chat.ID
	userID := int64(message.From.ID)

	username := ""
	if message.From.UserName != "" {
		username = message.From.UserName
	}

	text := ""
	if message.Text != "" {
		text = message.Text
	}

	chatType := ""
	if message.Chat.Type != "" {
		chatType = message.Chat.Type
	}

	h.logger.Info("Received message",
		slog.Int64("chat_id", chatID),
		slog.Int64("user_id", userID),
		slog.String("username", username),
		slog.String("text", text),
		slog.String("chat_type", chatType),
	)

	// В группах и супергруппах бот должен быть упомянут
	if message.Chat.Type == ChatTypeGroup || message.Chat.Type == ChatTypeSupergroup {
		if !h.isBotMentioned(message) {
			// Игнорируем сообщения без упоминания бота в группах
			return
		}
	}

	// Проверка авторизации
	if h.auth != nil && h.auth.IsEnabled() && !h.auth.IsAuthorized(userID) {
		h.handleAuthFlow(message)
		return
	}

	if message.IsCommand() {
		h.handleCommand(message)
		return
	}

	if message.Text != "" {
		h.handleTextMessage(ctx, message)
	}
}

// handleCommand обрабатывает команды бота
func (h *Handler) handleCommand(message *tgbotapi.Message) {
	if message == nil || message.Chat == nil {
		h.logger.Warn("Invalid message in handleCommand")
		return
	}

	chatID := message.Chat.ID
	command := message.Command()

	switch command {
	case "start":
		h.sendMessage(chatID, "👋 Привет! Я бот для скачивания видео.\n\n"+
			"Отправь мне ссылку на видео с:\n"+
			"• YouTube\n"+
			"• TikTok\n"+
			"• Instagram (Reels и обычные видео)\n\n"+
			"И я скачаю и отправлю тебе видео!")

	case "help":
		h.sendMessage(chatID, "📖 Помощь\n\n"+
			"Доступные команды:\n"+
			"/start - Начать работу с ботом\n"+
			"/help - Показать эту справку\n\n"+
			"Как использовать:\n"+
			"Просто отправь ссылку на видео, и я скачаю его для тебя!\n\n"+
			"Поддерживаемые платформы:\n"+
			"• YouTube (youtube.com, youtu.be)\n"+
			"• TikTok (tiktok.com)\n"+
			"• Instagram (instagram.com)")

	default:
		h.sendMessage(chatID, "❓ Неизвестная команда. Используй /help для справки.")
	}
}

// handleTextMessage обрабатывает текстовые сообщения со ссылками
func (h *Handler) handleTextMessage(ctx context.Context, message *tgbotapi.Message) {
	if message == nil || message.Chat == nil {
		h.logger.Warn("Invalid message in handleTextMessage")
		return
	}

	if message.Text == "" {
		return
	}

	chatID := message.Chat.ID
	text := strings.TrimSpace(message.Text)

	if message.Chat.Type == ChatTypeGroup || message.Chat.Type == ChatTypeSupergroup {
		if !h.isBotMentioned(message) {
			return
		}

		text = strings.TrimSpace(h.removeBotMentionFromText(text))
		if text == "" {
			return
		}
	}

	if !h.containsURL(text) {
		h.sendMessage(chatID, "❌ Пожалуйста, отправь валидную ссылку на видео.")
		return
	}

	url := h.extractURL(text)
	if url == "" {
		h.sendMessage(chatID, "❌ Не удалось извлечь ссылку из сообщения.")
		return
	}

	// Для YouTube показываем выбор качества
	if h.downloader == nil {
		h.logger.Error("downloader is nil")
		h.sendMessage(chatID, "❌ Внутренняя ошибка: сервис загрузки недоступен")
		return
	}

	if h.downloader.IsYouTubeURL(url) {
		statusMsg := h.sendMessage(chatID, "⏳ Анализирую видео...")
		if statusMsg == nil {
			h.logger.Error("Failed to send status message")
			return
		}
		messageID := h.safeMessageID(statusMsg)

		// Для Shorts сразу начинаем загрузку
		if h.downloader.IsYouTubeShorts(url) {
			h.editMsgSilent(chatID, messageID, "⏳ Загрузка Shorts...")
			downloadCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			h.downloadYouTubeVideo(downloadCtx, chatID, messageID, url, "best")
			return
		}

		// Для обычных видео показываем выбор качества
		h.showQualitySelection(chatID, url, messageID)
		return
	}

	// Для других платформ используем стандартный процесс
	statusMsg := h.sendMessage(chatID, "⏳ Запрос принят, начинаю загрузку видео...")
	downloadCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)

	req := &downloadRequest{
		ctx:             downloadCtx,
		cancel:          cancel,
		chatID:          chatID,
		url:             url,
		statusMessageID: h.safeMessageID(statusMsg),
		source:          "direct_message",
		originalMessage: message.MessageID,
	}

	if !h.enqueueDownload(req) {
		cancel()
		h.handleQueueOverflow(chatID, req.statusMessageID)
	}
}

func (h *Handler) enqueueDownload(req *downloadRequest) bool {
	select {
	case h.downloadQueue <- req:
		h.logger.Info("Download request enqueued",
			slog.Int64("chat_id", req.chatID),
			slog.String("url", req.url),
			slog.String("source", req.source),
		)
		return true
	default:
		h.logger.Warn("Download queue is full",
			slog.Int("queue_capacity", h.queueSizeLimit),
			slog.String("url", req.url),
		)
		return false
	}
}

func (h *Handler) handleQueueOverflow(chatID int64, statusMessageID int) {
	if statusMessageID != 0 {
		h.deleteMessage(chatID, statusMessageID)
	}
	h.sendMessage(chatID,
		"⚠️ Слишком много одновременных запросов. "+
			"Попробуй повторить через пару минут.")
}

func (h *Handler) processDownload(req *downloadRequest) {
	defer req.cancel()

	h.logger.Info("Processing download request",
		slog.Int64("chat_id", req.chatID),
		slog.String("url", req.url),
		slog.String("source", req.source),
	)

	// Используем DownloadWithType для определения типа медиа
	result, err := h.downloader.DownloadWithType(req.ctx, req.url)
	if err != nil {
		h.clearStatusMessage(req)
		h.logger.Error("Failed to download media",
			slog.String("url", req.url),
			slog.Any("error", err),
		)
		errMsg := fmt.Sprintf("❌ Ошибка при загрузке медиа:\n%s", err.Error())
		h.logger.Info("Sending error message to user", slog.String("message", errMsg))
		msg := h.sendPlainMessage(req.chatID, errMsg)
		if msg == nil {
			h.logger.Error("Failed to send error message to user")
		}
		return
	}
	defer func() {
		if err := h.downloader.Cleanup(result.FilePath); err != nil {
			h.logger.Warn("Failed to cleanup file", slog.String("file", result.FilePath), slog.Any("error", err))
		}
	}()

	h.clearStatusMessage(req)

	// Отправляем медиа в зависимости от типа
	switch result.Type {
	case downloader.MediaTypeVideo:
		if err := h.sendVideo(req.chatID, result.FilePath); err != nil {
			h.logger.Error("Failed to send video",
				slog.String("file", result.FilePath),
				slog.Any("error", err),
			)
			h.sendMessage(req.chatID, fmt.Sprintf("❌ Ошибка при отправке видео: %s", err.Error()))
			return
		}
		h.logger.Info("Video delivered successfully",
			slog.Int64("chat_id", req.chatID),
			slog.String("url", req.url),
		)

	case downloader.MediaTypePhoto:
		if err := h.sendPhoto(req.chatID, result.FilePath); err != nil {
			h.logger.Error("Failed to send photo",
				slog.String("file", result.FilePath),
				slog.Any("error", err),
			)
			h.sendMessage(req.chatID, fmt.Sprintf("❌ Ошибка при отправке фото: %s", err.Error()))
			return
		}
		h.logger.Info("Photo delivered successfully",
			slog.Int64("chat_id", req.chatID),
			slog.String("url", req.url),
		)

	case downloader.MediaTypeAudio:
		if err := h.sendAudio(req.chatID, result.FilePath); err != nil {
			h.logger.Error("Failed to send audio",
				slog.String("file", result.FilePath),
				slog.Any("error", err),
			)
			h.sendMessage(req.chatID, fmt.Sprintf("❌ Ошибка при отправке аудио: %s", err.Error()))
			return
		}
		h.logger.Info("Audio delivered successfully",
			slog.Int64("chat_id", req.chatID),
			slog.String("url", req.url),
		)

	default:
		h.logger.Error("Unknown media type",
			slog.String("type", string(result.Type)),
			slog.String("file", result.FilePath),
		)
		h.sendMessage(req.chatID, "❌ Неподдерживаемый тип медиа.")
		return
	}

	h.deleteOriginalMessage(req)
}

func (h *Handler) clearStatusMessage(req *downloadRequest) {
	if req.statusMessageID != 0 {
		h.deleteMessage(req.chatID, req.statusMessageID)
		req.statusMessageID = 0
	}
}

func (h *Handler) deleteOriginalMessage(req *downloadRequest) {
	if req.originalMessage != 0 {
		h.deleteMessage(req.chatID, req.originalMessage)
		req.originalMessage = 0
	}
}

// handleAuthFlow обрабатывает сообщения от неавторизованных пользователей
func (h *Handler) handleAuthFlow(message *tgbotapi.Message) {
	if message == nil || message.From == nil || message.Chat == nil {
		h.logger.Warn("Invalid message in handleAuthFlow")
		return
	}

	chatID := message.Chat.ID
	userID := int64(message.From.ID)

	text := ""
	if message.Text != "" {
		text = h.removeBotMentionFromText(message.Text)
	}

	// Если это команда или пустое сообщение — просто просим отправить токен
	if text == "" || message.IsCommand() {
		h.sendMessage(chatID,
			"🔒 Этот бот доступен только по токену доступа.\n"+
				"Отправь мне токен, который выдал администратор.")
		return
	}

	// Пытаемся авторизовать пользователя по присланному тексту
	if ok := h.auth.TryAuthorize(userID, text); !ok {
		h.sendMessage(chatID,
			"❌ Неверный токен доступа.\nПроверь токен или обратись к администратору.")
		return
	}

	h.sendMessage(chatID,
		"✅ Авторизация успешна! Теперь ты можешь отправлять ссылки на видео.")
}

func (h *Handler) handleInlineQuery(inlineQuery *tgbotapi.InlineQuery) {
	if inlineQuery == nil {
		h.logger.Warn("Received nil inline query")
		return
	}

	if inlineQuery.From == nil {
		h.logger.Warn("Received inline query without From field", slog.String("query_id", inlineQuery.ID))
		return
	}

	queryText := strings.TrimSpace(inlineQuery.Query)
	userID := int64(inlineQuery.From.ID)

	username := ""
	if inlineQuery.From.UserName != "" {
		username = inlineQuery.From.UserName
	}

	h.logger.Info("Received inline query",
		slog.String("query_id", inlineQuery.ID),
		slog.Int64("user_id", userID),
		slog.String("username", username),
		slog.String("query", queryText),
	)

	// Если включена авторизация и пользователь не авторизован - подсказка
	if h.auth != nil && h.auth.IsEnabled() &&
		!h.auth.IsAuthorized(userID) {
		results := []interface{}{
			tgbotapi.NewInlineQueryResultArticle(
				inlineQuery.ID+"-auth",
				"Требуется авторизация",
				"Этот бот защищён.\n"+
					"Открой личный чат с ботом и отправь токен доступа, "+
					"который выдал администратор.",
			),
		}

		inlineConfig := tgbotapi.InlineConfig{
			InlineQueryID: inlineQuery.ID,
			Results:       results,
			CacheTime:     0,
			IsPersonal:    true,
		}

		if _, err := h.bot.Request(inlineConfig); err != nil {
			h.logger.Error("Failed to answer inline auth query",
				slog.String("query_id", inlineQuery.ID),
				slog.Any("error", err),
			)
		}
		return
	}

	results := h.buildInlineResults(inlineQuery.ID, queryText)

	inlineConfig := tgbotapi.InlineConfig{
		InlineQueryID: inlineQuery.ID,
		Results:       results,
		CacheTime:     0,
		IsPersonal:    true,
	}

	if _, err := h.bot.Request(inlineConfig); err != nil {
		h.logger.Error("Failed to answer inline query",
			slog.String("query_id", inlineQuery.ID),
			slog.Any("error", err),
		)
	}
}

func (h *Handler) buildInlineResults(queryID, rawQuery string) []interface{} {
	var results []interface{}

	if url := h.extractURL(rawQuery); url != "" && h.containsURL(url) {
		messageText := fmt.Sprintf(
			"⏳ Запрос на скачивание:\n%s\n\nБот отправит видео в личные сообщения.",
			url,
		)
		result := tgbotapi.NewInlineQueryResultArticle(queryID+"-download", "Скачать видео", messageText)
		result.Description = PlatformsSupported
		results = append(results, result)
	} else {
		helpResult := tgbotapi.NewInlineQueryResultArticle(
			queryID+"-help",
			"Укажи ссылку на видео",
			"Пример: https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		)
		helpResult.Description = PlatformsSupported
		results = append(results, helpResult)
	}

	return results
}

func (h *Handler) handleChosenInlineResult(ctx context.Context, result *tgbotapi.ChosenInlineResult) {
	if result == nil {
		h.logger.Warn("Received nil chosen inline result")
		return
	}

	if result.From == nil {
		h.logger.Warn("Received chosen inline result without From field")
		return
	}

	url := h.extractURL(result.Query)
	if url == "" {
		h.logger.Warn("Chosen inline result without URL", slog.String("query", result.Query))
		return
	}

	chatID := int64(result.From.ID)
	userID := chatID

	if h.auth != nil && h.auth.IsEnabled() && !h.auth.IsAuthorized(userID) {
		h.logger.Warn("Unauthenticated user tried to use inline chosen result",
			slog.Int64("user_id", userID),
		)
		h.sendMessage(chatID,
			"🔒 Этот бот защищён. Отправь токен доступа в личные сообщения бота, "+
				"чтобы продолжить использование.")
		return
	}
	statusMsg := h.sendMessage(chatID, "⏳ Обработка inline-запроса, загружаю видео...")
	downloadCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)

	req := &downloadRequest{
		ctx:             downloadCtx,
		cancel:          cancel,
		chatID:          chatID,
		url:             url,
		statusMessageID: h.safeMessageID(statusMsg),
		source:          "inline_mode",
	}

	if !h.enqueueDownload(req) {
		cancel()
		h.handleQueueOverflow(chatID, req.statusMessageID)
	}
}

func (h *Handler) safeMessageID(msg *tgbotapi.Message) int {
	if msg == nil {
		return 0
	}
	return msg.MessageID
}

// maxAllowedFileSize возвращает максимальный размер файла для Telegram (50MB)
func (h *Handler) maxAllowedFileSize() int64 {
	return int64(50 * 1024 * 1024)
}

// handleCallbackQuery обрабатывает callback queries от inline keyboards
func (h *Handler) handleCallbackQuery(ctx context.Context, callbackQuery *tgbotapi.CallbackQuery) {
	if callbackQuery == nil || callbackQuery.Message == nil {
		h.logger.Warn("Callback query or message is nil")
		return
	}

	// Проверяем инициализацию handler
	if h.stateManager == nil {
		h.logger.Error("stateManager is nil in handleCallbackQuery")
		return
	}
	if h.downloader == nil {
		h.logger.Error("downloader is nil in handleCallbackQuery")
		return
	}
	if h.bot == nil {
		h.logger.Error("bot is nil in handleCallbackQuery")
		return
	}

	chatID := callbackQuery.Message.Chat.ID
	messageID := callbackQuery.Message.MessageID
	data := callbackQuery.Data

	h.logger.Info("Received callback query",
		slog.Int64("chat_id", chatID),
		slog.Int("message_id", messageID),
		slog.String("data", data),
	)

	// Отвечаем на callback чтобы убрать "часики"
	callback := tgbotapi.NewCallback(callbackQuery.ID, "")
	if _, err := h.bot.Request(callback); err != nil {
		h.logger.Error("Failed to answer callback", slog.Any("error", err))
	}

	// Проверяем, что это callback для выбора качества
	if !strings.HasPrefix(data, "yt_quality:") {
		return
	}

	// Парсим callback data: "yt_quality:{video_id}:{quality}"
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return
	}

	_ = parts[1] // videoID не используется напрямую, хранится в state
	quality := parts[2]

	h.logger.Info("Parsed callback data", slog.String("quality", quality))

	// Получаем сохраненное состояние
	state, exists := h.stateManager.Get(chatID, messageID)
	if !exists {
		h.logger.Warn("State not found for callback",
			slog.Int64("chat_id", chatID),
			slog.Int("message_id", messageID))
		h.editMsgSilent(chatID, messageID,
			"❌ Время выбора качества истекло. Отправь ссылку заново.")
		return
	}

	h.logger.Info("Found state", slog.String("video_url", state.VideoURL))

	// Удаляем inline keyboard
	if err := h.editMessageReplyMarkup(chatID, messageID, nil); err != nil {
		h.logger.Error("Failed to remove keyboard", slog.Any("error", err))
	}

	// Для Shorts просто скачиваем без выбора качества
	if h.downloader.IsYouTubeShorts(state.VideoURL) {
		h.editMsgSilent(chatID, messageID, "⏳ Загрузка Shorts...")
		h.downloadYouTubeVideo(ctx, chatID, messageID, state.VideoURL, "best")
		return
	}

	// Показываем прогресс и начинаем загрузку
	h.editMsgSilent(chatID, messageID, "⏳ Начинаю загрузку...")
	h.downloadYouTubeVideo(ctx, chatID, messageID, state.VideoURL, quality)
}

// showQualitySelection показывает inline keyboard с выбором качества
func (h *Handler) showQualitySelection(chatID int64, videoURL string, messageID int) {
	// Проверяем инициализацию
	if h.downloader == nil {
		h.logger.Error("downloader is nil in showQualitySelection")
		h.sendMessage(chatID, "❌ Внутренняя ошибка: сервис загрузки недоступен")
		return
	}
	if h.stateManager == nil {
		h.logger.Error("stateManager is nil in showQualitySelection")
		h.sendMessage(chatID, "❌ Внутренняя ошибка: менеджер состояний недоступен")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Получаем доступные качества
	qualities, err := h.downloader.GetYouTubeQualities(ctx, videoURL)
	if err != nil {
		h.logger.Error("Failed to get qualities", slog.String("url", videoURL), slog.Any("error", err))
		h.sendMessage(chatID, "❌ Не удалось получить информацию о видео.")
		return
	}

	if len(qualities) == 0 {
		h.sendMessage(chatID, "❌ Нет доступных форматов для этого видео.")
		return
	}

	// Создаем inline keyboard
	var rows [][]tgbotapi.InlineKeyboardButton
	videoID := h.downloader.GetYouTubeVideoID(videoURL)

	// Фильтруем и группируем качества
	qualityOptions := map[string]bool{"2160": false, "1440": false, "1080": false, "720": false, "480": false, "audio": false}

	for _, q := range qualities {
		if q.IsAudioOnly {
			qualityOptions["audio"] = true
		} else if q.Height >= 2160 {
			qualityOptions["2160"] = true
		} else if q.Height >= 1440 {
			qualityOptions["1440"] = true
		} else if q.Height >= 1080 {
			qualityOptions["1080"] = true
		} else if q.Height >= 720 {
			qualityOptions["720"] = true
		} else if q.Height >= 480 {
			qualityOptions["480"] = true
		}
	}

	// Создаем кнопки (от высокого к низкому качеству)
	var buttons []tgbotapi.InlineKeyboardButton

	if qualityOptions["2160"] {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(
			"🎬 4K",
			fmt.Sprintf("yt_quality:%s:2160", videoID),
		))
	}
	if qualityOptions["1440"] {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(
			"🎬 2K",
			fmt.Sprintf("yt_quality:%s:1440", videoID),
		))
	}
	if qualityOptions["1080"] {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(
			"📺 1080p",
			fmt.Sprintf("yt_quality:%s:1080", videoID),
		))
	}
	if qualityOptions["720"] {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(
			"📱 720p",
			fmt.Sprintf("yt_quality:%s:720", videoID),
		))
	}
	if qualityOptions["480"] {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(
			"📱 480p",
			fmt.Sprintf("yt_quality:%s:480", videoID),
		))
	}
	if qualityOptions["audio"] {
		buttons = append(buttons, tgbotapi.NewInlineKeyboardButtonData(
			"🎵 Audio",
			fmt.Sprintf("yt_quality:%s:audio", videoID),
		))
	}

	// Разбиваем на ряды по 2 кнопки
	for i := 0; i < len(buttons); i += 2 {
		end := i + 2
		if end > len(buttons) {
			end = len(buttons)
		}
		rows = append(rows, buttons[i:end])
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	// Редактируем сообщение с inline keyboard
	text := "🎥 Выберите качество видео:"
	if _, err := h.editMessageTextAndMarkup(chatID, messageID, text, &keyboard); err != nil {
		// Если редактирование не удалось, отправляем новое сообщение
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ReplyMarkup = keyboard
		sentMsg, err := h.bot.Send(msg)
		if err != nil {
			h.logger.Error("Failed to send quality selection", slog.Any("error", err))
			return
		}
		messageID = sentMsg.MessageID
	}

	// Сохраняем состояние
	h.stateManager.Set(chatID, messageID, videoURL)
}

// downloadYouTubeVideo скачивает YouTube видео с выбранным качеством
func (h *Handler) downloadYouTubeVideo(
	ctx context.Context,
	chatID int64,
	messageID int,
	videoURL string,
	quality string,
) {
	// Создаем callback для обновления прогресса
	lastUpdate := time.Now()
	progressCallback := func(percent int, downloaded int64, total int64, speed string, eta string) {
		// Обновляем не чаще чем раз в 3 секунды
		if time.Since(lastUpdate) < 3*time.Second {
			return
		}
		lastUpdate = time.Now()

		progressBar := h.formatProgressBar(percent)
		text := fmt.Sprintf(
			"🎥 Загрузка видео...\n%s\n%d%%",
			progressBar,
			percent,
		)

		if total > 0 {
			text += fmt.Sprintf("\n📦 %.1f / %.1f MB", float64(downloaded)/(1024*1024), float64(total)/(1024*1024))
		}
		if speed != "" {
			text += fmt.Sprintf("\n⚡ %s", speed)
		}
		if eta != "" {
			text += fmt.Sprintf("\n⏱ Осталось: %s", eta)
		}

		h.editMsgSilent(chatID, messageID, text)
	}

	// Скачиваем видео
	result, err := h.downloader.DownloadYouTubeWithQuality(ctx, videoURL, quality, progressCallback)
	if err != nil {
		h.editMsgSilent(chatID, messageID, fmt.Sprintf("❌ Ошибка при загрузке: %v", err))
		return
	}

	defer func() {
		if err := h.downloader.Cleanup(result.FilePath); err != nil {
			h.logger.Warn("Failed to cleanup file", slog.String("file", result.FilePath), slog.Any("error", err))
		}
	}()

	// Отправляем файл
	h.editMsgSilent(chatID, messageID, "📤 Отправка видео...")

	var sendErr error
	switch result.Type {
	case downloader.MediaTypeVideo:
		sendErr = h.sendVideo(chatID, result.FilePath)
	case downloader.MediaTypeAudio:
		sendErr = h.sendAudio(chatID, result.FilePath)
	default:
		sendErr = h.sendVideo(chatID, result.FilePath)
	}

	if sendErr != nil {
		h.editMsgSilent(chatID, messageID, fmt.Sprintf("❌ Ошибка при отправке: %v", sendErr))
		return
	}

	// Удаляем сообщение о загрузке
	h.deleteMessage(chatID, messageID)
}

// formatProgressBar создает визуальный прогресс-бар
func (h *Handler) formatProgressBar(percent int) string {
	filled := percent / 10
	empty := 10 - filled

	bar := "["
	for i := 0; i < filled; i++ {
		bar += "█"
	}
	for i := 0; i < empty; i++ {
		bar += "░"
	}
	bar += "]"

	return bar
}

// editMessageText редактирует текст сообщения
func (h *Handler) editMessageText(chatID int64, messageID int, text string) error {
	edit := tgbotapi.NewEditMessageText(chatID, messageID, text)
	edit.ParseMode = ParseModeHTML
	_, err := h.bot.Request(edit)
	return err
}

// editMessageReplyMarkup редактирует inline keyboard сообщения
func (h *Handler) editMessageReplyMarkup(chatID int64, messageID int, markup *tgbotapi.InlineKeyboardMarkup) error {
	if markup == nil {
		// Если markup nil, редактируем текст сообщения без keyboard (это удалит keyboard)
		// Сначала получаем текущий текст сообщения
		msg := tgbotapi.NewEditMessageText(chatID, messageID, "⏳ Загрузка...")
		_, err := h.bot.Request(msg)
		return err
	}
	edit := tgbotapi.NewEditMessageReplyMarkup(
		chatID, messageID, *markup)
	_, err := h.bot.Request(edit)
	return err
}

// editMessageTextAndMarkup редактирует текст и keyboard
func (h *Handler) editMessageTextAndMarkup(
	chatID int64, messageID int, text string,
	markup *tgbotapi.InlineKeyboardMarkup,
) (*tgbotapi.Message, error) {
	edit := tgbotapi.NewEditMessageText(chatID, messageID, text)
	edit.ParseMode = ParseModeHTML
	edit.ReplyMarkup = markup
	_, err := h.bot.Request(edit)
	if err != nil {
		return nil, err
	}
	// Конвертируем APIResponse в Message
	// Note: go-telegram-bot-api не предоставляет прямой способ получить Message из Request
	// поэтому возвращаем nil
	return nil, nil
}

// isBotMentioned проверяет, упомянут ли бот в сообщении
func (h *Handler) isBotMentioned(message *tgbotapi.Message) bool {
	if h.botUsername == "" || message == nil {
		return false
	}

	// Проверяем наличие текста
	if message.Text == "" {
		return false
	}

	// Проверяем entities (упоминания через @username)
	if len(message.Entities) > 0 {
		for _, entity := range message.Entities {
			if entity.Type == "mention" {
				// Проверяем границы перед обращением к строке
				if entity.Offset >= 0 && entity.Offset+entity.Length <= len(message.Text) {
					mention := message.Text[entity.Offset : entity.Offset+entity.Length]
					// Убираем @ и сравниваем
					if strings.TrimPrefix(mention, "@") == h.botUsername {
						return true
					}
				}
			}
		}
	}

	// Также проверяем текст напрямую (на случай, если entities не сработали)
	text := strings.ToLower(message.Text)
	botMention := "@" + strings.ToLower(h.botUsername)
	return strings.Contains(text, botMention)
}

func (h *Handler) removeBotMentionFromText(text string) string {
	if h.botUsername == "" {
		return text
	}

	target := "@" + h.botUsername
	words := strings.Fields(text)
	cleaned := make([]string, 0, len(words))
	for _, word := range words {
		if strings.EqualFold(word, target) {
			continue
		}
		cleaned = append(cleaned, word)
	}

	return strings.Join(cleaned, " ")
}

// containsURL проверяет, содержит ли текст URL
func (h *Handler) containsURL(text string) bool {
	return strings.Contains(text, "http://") ||
		strings.Contains(text, "https://") ||
		strings.Contains(text, "youtube.com") ||
		strings.Contains(text, "youtu.be") ||
		strings.Contains(text, "tiktok.com") ||
		strings.Contains(text, "instagram.com")
}

// extractURL извлекает первый URL из текста
func (h *Handler) extractURL(text string) string {
	words := strings.Fields(text)
	for _, word := range words {
		if strings.HasPrefix(word, "http://") || strings.HasPrefix(word, "https://") {
			// Убираем возможные знаки препинания в конце
			word = strings.TrimRight(word, ".,;:!?")
			return word
		}
	}
	return ""
}

// sendMessage отправляет текстовое сообщение
func (h *Handler) sendMessage(chatID int64, text string) *tgbotapi.Message {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = ParseModeHTML

	sentMsg, err := h.bot.Send(msg)
	if err != nil {
		h.logger.Error("Failed to send message",
			slog.Int64("chat_id", chatID),
			slog.Any("error", err),
		)
		return nil
	}
	return &sentMsg
}

// sendPlainMessage отправляет текстовое сообщение без HTML форматирования
func (h *Handler) sendPlainMessage(chatID int64, text string) *tgbotapi.Message {
	msg := tgbotapi.NewMessage(chatID, text)
	// Не используем HTML чтобы избежать проблем с спецсимволами

	sentMsg, err := h.bot.Send(msg)
	if err != nil {
		h.logger.Error("Failed to send plain message",
			slog.Int64("chat_id", chatID),
			slog.Any("error", err),
		)
		return nil
	}
	return &sentMsg
}

// deleteMessage удаляет сообщение
func (h *Handler) deleteMessage(chatID int64, messageID int) {
	deleteMsg := tgbotapi.NewDeleteMessage(chatID, messageID)
	if _, err := h.bot.Request(deleteMsg); err != nil {
		h.logger.Warn("Failed to delete message",
			slog.Int64("chat_id", chatID),
			slog.Int("message_id", messageID),
			slog.Any("error", err),
		)
	}
}

// sendVideo отправляет видео файл
func (h *Handler) sendVideo(chatID int64, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Получаем информацию о файле
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	// Проверяем размер файла перед отправкой
	maxAllowed := h.maxAllowedFileSize()
	if fileInfo.Size() > maxAllowed {
		return fmt.Errorf("file size %d exceeds maximum allowed size %d",
			fileInfo.Size(), maxAllowed)
	}

	// Используем FileReader для потоковой отправки
	// вместо загрузки всего файла в память
	fileReader := tgbotapi.FileReader{
		Name:   fileInfo.Name(),
		Reader: file,
	}

	// Отправляем видео
	video := tgbotapi.NewVideo(chatID, fileReader)
	video.SupportsStreaming = true

	h.logger.Info("Sending video",
		slog.Int64("chat_id", chatID),
		slog.String("file", filePath),
		slog.Int64("size", fileInfo.Size()),
	)

	if _, err := h.bot.Send(video); err != nil {
		return fmt.Errorf("failed to send video: %w", err)
	}

	h.logger.Info("Video sent successfully", slog.Int64("chat_id", chatID))
	return nil
}

// sendPhoto отправляет фото файл
func (h *Handler) sendPhoto(chatID int64, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Получаем информацию о файле
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	// Проверяем размер файла перед отправкой (для фото лимит 50MB как и для видео)
	const maxPhotoSize = int64(50 * 1024 * 1024)
	if fileInfo.Size() > maxPhotoSize {
		return fmt.Errorf("photo size %d exceeds maximum allowed size %d", fileInfo.Size(), maxPhotoSize)
	}

	// Используем FileReader для потоковой отправки
	fileReader := tgbotapi.FileReader{
		Name:   fileInfo.Name(),
		Reader: file,
	}

	// Отправляем фото
	photo := tgbotapi.NewPhoto(chatID, fileReader)

	h.logger.Info("Sending photo",
		slog.Int64("chat_id", chatID),
		slog.String("file", filePath),
		slog.Int64("size", fileInfo.Size()),
	)

	if _, err := h.bot.Send(photo); err != nil {
		return fmt.Errorf("failed to send photo: %w", err)
	}

	h.logger.Info("Photo sent successfully", slog.Int64("chat_id", chatID))
	return nil
}

// sendAudio отправляет аудио файл
func (h *Handler) sendAudio(chatID int64, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Получаем информацию о файле
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	// Проверяем размер файла перед отправкой (для аудио лимит 50MB)
	maxAllowed := h.maxAllowedFileSize()
	if fileInfo.Size() > maxAllowed {
		return fmt.Errorf("audio size %d exceeds maximum allowed size %d", fileInfo.Size(), maxAllowed)
	}

	// Используем FileReader для потоковой отправки
	fileReader := tgbotapi.FileReader{
		Name:   fileInfo.Name(),
		Reader: file,
	}

	// Отправляем аудио
	audio := tgbotapi.NewAudio(chatID, fileReader)

	h.logger.Info("Sending audio",
		slog.Int64("chat_id", chatID),
		slog.String("file", filePath),
		slog.Int64("size", fileInfo.Size()),
	)

	if _, err := h.bot.Send(audio); err != nil {
		return fmt.Errorf("failed to send audio: %w", err)
	}

	h.logger.Info("Audio sent successfully", slog.Int64("chat_id", chatID))
	return nil
}

// editSilent - обертка, которая не возвращаяет ошибку, обход линтера
func (h *Handler) editMsgSilent(chatID int64, msgID int, text string) {
	if err := h.editMessageText(chatID, msgID, text); err != nil {
		// Здесь мы просто логируем проблему, чтобы она не пропала
		slog.Error("failed to edit message", "error", err, "msgID", msgID)
	}
}
