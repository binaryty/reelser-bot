package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"

	"github.com/reelser-bot/internal/services/auth"
	"github.com/reelser-bot/internal/services/downloader"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Bot представляет Telegram-бота
type Bot struct {
	api           *tgbotapi.BotAPI
	handler       *Handler
	logger        *slog.Logger
	ctx           context.Context
	cancel        context.CancelFunc
	updateWorkers int
	updateQueue   chan tgbotapi.Update
}

// NewBot создает новый экземпляр бота
func NewBot(
	token string,
	logger *slog.Logger,
	downloader *downloader.Service,
	authService *auth.Service,
	maxVideoSizeMB int,
	workerCount int,
) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot API: %w", err)
	}

	botUsername := api.Self.UserName
	handler := NewHandler(api, botUsername, logger, downloader, authService, maxVideoSizeMB, workerCount)

	ctx, cancel := context.WithCancel(context.Background())

	// Количество воркеров для обработки апдейтов (по умолчанию количество CPU)
	updateWorkers := runtime.NumCPU()
	if updateWorkers < 2 {
		updateWorkers = 2 // минимум 2 воркера
	}
	if updateWorkers > 10 {
		updateWorkers = 10 // максимум 10 воркеров
	}

	// Размер очереди апдейтов = количество воркеров * 2
	updateQueueSize := updateWorkers * 2

	bot := &Bot{
		api:           api,
		handler:       handler,
		logger:        logger,
		ctx:           ctx,
		cancel:        cancel,
		updateWorkers: updateWorkers,
		updateQueue:   make(chan tgbotapi.Update, updateQueueSize),
	}

	logger.Info("Bot initialized",
		slog.String("username", api.Self.UserName),
		slog.Int64("id", int64(api.Self.ID)),
		slog.Int("update_workers", updateWorkers),
		slog.Int("update_queue_size", updateQueueSize),
	)

	return bot, nil
}

// Start запускает бота
func (b *Bot) Start() error {
	b.logger.Info("Starting bot...")

	// Запускаем пул воркеров для обработки апдейтов
	for i := 0; i < b.updateWorkers; i++ {
		workerID := i + 1
		go func(id int) {
			defer func() {
				if r := recover(); r != nil {
					b.logger.Error("Panic recovered in update worker",
						slog.Int("worker_id", id),
						slog.Any("panic", r),
					)
				}
			}()

			b.logger.Info("Update worker started", slog.Int("worker_id", id))
			for {
				select {
				case <-b.ctx.Done():
					b.logger.Info("Update worker stopped", slog.Int("worker_id", id))
					return
				case update := <-b.updateQueue:
					b.handler.HandleUpdate(b.ctx, update)
				}
			}
		}(workerID)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case <-b.ctx.Done():
			b.logger.Info("Bot context cancelled, stopping...")
			return nil

		case update := <-updates:
			// Пытаемся добавить апдейт в очередь
			select {
			case b.updateQueue <- update:
				// Апдейт успешно добавлен в очередь
			default:
				// Очередь переполнена - логируем предупреждение
				b.logger.Warn("Update queue is full, dropping update",
					slog.Int("queue_size", cap(b.updateQueue)),
				)
			}
		}
	}
}

// Stop останавливает бота
func (b *Bot) Stop() {
	b.logger.Info("Stopping bot...")
	b.cancel()
	b.api.StopReceivingUpdates()
}
