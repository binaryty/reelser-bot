package telegram

import (
	"context"
	"fmt"

	"github.com/reelser-bot/internal/services/auth"
	"github.com/reelser-bot/internal/services/downloader"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

// Bot представляет Telegram-бота
type Bot struct {
	api     *tgbotapi.BotAPI
	handler *Handler
	logger  *zap.Logger
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewBot создает новый экземпляр бота
func NewBot(
	token string,
	logger *zap.Logger,
	downloader *downloader.Service,
	authService *auth.Service,
	maxVideoSizeMB int,
	workerCount int,
) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot API: %w", err)
	}

	handler := NewHandler(api, logger, downloader, authService, maxVideoSizeMB, workerCount)

	ctx, cancel := context.WithCancel(context.Background())

	bot := &Bot{
		api:     api,
		handler: handler,
		logger:  logger,
		ctx:     ctx,
		cancel:  cancel,
	}

	logger.Info("Bot initialized",
		zap.String("username", api.Self.UserName),
		zap.Int64("id", int64(api.Self.ID)),
	)

	return bot, nil
}

// Start запускает бота
func (b *Bot) Start() error {
	b.logger.Info("Starting bot...")

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case <-b.ctx.Done():
			b.logger.Info("Bot context cancelled, stopping...")
			return nil

		case update := <-updates:
			go b.handler.HandleUpdate(b.ctx, update)
		}
	}
}

// Stop останавливает бота
func (b *Bot) Stop() {
	b.logger.Info("Stopping bot...")
	b.cancel()
	b.api.StopReceivingUpdates()
}
