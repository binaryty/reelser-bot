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
	maxVideoSize   int64 // в байтах
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
		maxVideoSize:   int64(maxVideoSizeMB) * 1024 * 1024, // конвертируем в байты
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
func (h *Handler) HandleUpdate(ctx context.Context, update tgbotapi.Update) {
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
	case update.InlineQuery != nil:
		h.handleInlineQuery(ctx, update.InlineQuery)
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
	if message.Chat.Type == "group" || message.Chat.Type == "supergroup" {
		if !h.isBotMentioned(message) {
			// Игнорируем сообщения без упоминания бота в группах
			return
		}
	}

	// Проверка авторизации
	if h.auth != nil && h.auth.IsEnabled() && !h.auth.IsAuthorized(userID) {
		h.handleAuthFlow(ctx, message)
		return
	}

	if message.IsCommand() {
		h.handleCommand(ctx, message)
		return
	}

	if message.Text != "" {
		h.handleTextMessage(ctx, message)
	}
}

// handleCommand обрабатывает команды бота
func (h *Handler) handleCommand(ctx context.Context, message *tgbotapi.Message) {
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

	if message.Chat.Type == "group" || message.Chat.Type == "supergroup" {
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
	h.sendMessage(chatID, "⚠️ Слишком много одновременных запросов. Попробуй повторить через пару минут.")
}

func (h *Handler) processDownload(req *downloadRequest) {
	defer req.cancel()

	h.logger.Info("Processing download request",
		slog.Int64("chat_id", req.chatID),
		slog.String("url", req.url),
		slog.String("source", req.source),
	)

	filePath, err := h.downloader.Download(req.ctx, req.url)
	if err != nil {
		h.clearStatusMessage(req)
		h.logger.Error("Failed to download video",
			slog.String("url", req.url),
			slog.Any("error", err),
		)
		h.sendMessage(req.chatID, fmt.Sprintf("❌ Ошибка при загрузке видео: %s", err.Error()))
		return
	}
	defer func() {
		if err := h.downloader.Cleanup(filePath); err != nil {
			h.logger.Warn("Failed to cleanup file", slog.String("file", filePath), slog.Any("error", err))
		}
	}()

	h.clearStatusMessage(req)

	fileSize, err := h.downloader.GetFileSize(filePath)
	if err != nil {
		h.logger.Error("Failed to get file size", slog.String("file", filePath), slog.Any("error", err))
		h.sendMessage(req.chatID, "❌ Ошибка при проверке размера файла.")
		return
	}

	maxAllowed := h.maxAllowedFileSize()
	if fileSize > maxAllowed {
		h.sendMessage(req.chatID, fmt.Sprintf(
			"❌ Видео слишком большое (%.2f MB). Ограничение Telegram %.0f MB.",
			float64(fileSize)/(1024*1024),
			float64(maxAllowed)/(1024*1024),
		))
		return
	}

	if err := h.sendVideo(req.chatID, filePath); err != nil {
		h.logger.Error("Failed to send video",
			slog.String("file", filePath),
			slog.Any("error", err),
		)
		h.sendMessage(req.chatID, fmt.Sprintf("❌ Ошибка при отправке видео: %s", err.Error()))
		return
	}

	h.logger.Info("Video delivered successfully",
		slog.Int64("chat_id", req.chatID),
		slog.String("url", req.url),
	)

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
func (h *Handler) handleAuthFlow(ctx context.Context, message *tgbotapi.Message) {
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
		h.sendMessage(chatID, "🔒 Этот бот доступен только по токену доступа.\nОтправь мне токен, который выдал администратор.")
		return
	}

	// Пытаемся авторизовать пользователя по присланному тексту
	if ok := h.auth.TryAuthorize(userID, text); !ok {
		h.sendMessage(chatID, "❌ Неверный токен доступа.\nПроверь токен или обратись к администратору.")
		return
	}

	h.sendMessage(chatID, "✅ Авторизация успешна! Теперь ты можешь отправлять ссылки на видео.")
}

func (h *Handler) handleInlineQuery(ctx context.Context, inlineQuery *tgbotapi.InlineQuery) {
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

	// Если включена авторизация и пользователь не авторизован — показываем подсказку
	if h.auth != nil && h.auth.IsEnabled() && !h.auth.IsAuthorized(userID) {
		results := []interface{}{
			tgbotapi.NewInlineQueryResultArticle(
				inlineQuery.ID+"-auth",
				"Требуется авторизация",
				"Этот бот защищён.\nОткрой личный чат с ботом и отправь токен доступа, который выдал администратор.",
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
		messageText := fmt.Sprintf("⏳ Запрос на скачивание:\n%s\n\nБот отправит видео в личные сообщения.", url)
		result := tgbotapi.NewInlineQueryResultArticle(queryID+"-download", "Скачать видео", messageText)
		result.Description = "Поддерживаются YouTube, TikTok и Instagram"
		results = append(results, result)
	} else {
		helpResult := tgbotapi.NewInlineQueryResultArticle(
			queryID+"-help",
			"Укажи ссылку на видео",
			"Пример: https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		)
		helpResult.Description = "Поддерживаются YouTube, TikTok и Instagram"
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
		h.sendMessage(chatID, "🔒 Этот бот защищён. Отправь токен доступа в личные сообщения бота, чтобы продолжить использование.")
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

func (h *Handler) maxAllowedFileSize() int64 {
	const telegramLimit = int64(50 * 1024 * 1024)
	if h.maxVideoSize <= 0 || h.maxVideoSize > telegramLimit {
		return telegramLimit
	}
	return h.maxVideoSize
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

	target := "@" + strings.ToLower(h.botUsername)
	words := strings.Fields(text)
	cleaned := make([]string, 0, len(words))
	for _, word := range words {
		if strings.ToLower(word) == target {
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
	msg.ParseMode = "HTML"

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

	// Создаем FileBytes для отправки
	fileBytes := tgbotapi.FileBytes{
		Name:  fileInfo.Name(),
		Bytes: make([]byte, fileInfo.Size()),
	}

	// Читаем файл
	if _, err := file.Read(fileBytes.Bytes); err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Отправляем видео
	video := tgbotapi.NewVideo(chatID, fileBytes)
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
