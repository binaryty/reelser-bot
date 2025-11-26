package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/reelser-bot/internal/config"
	"github.com/reelser-bot/internal/services/auth"
	"github.com/reelser-bot/internal/services/downloader"
	"github.com/reelser-bot/internal/transport/telegram"
)

func main() {
	// Инициализация логгера
	logger := initLogger()

	logger.Info("Starting application...")

	// Загрузка конфигурации
	cfg, err := config.Load()
	if err != nil {
		logger.Error("Failed to load configuration", slog.Any("error", err))
		os.Exit(1)
	}

	logger.Info("Configuration loaded successfully")

	// Создание временной директории
	if err := os.MkdirAll(cfg.Download.TempDir, 0755); err != nil {
		logger.Error("Failed to create temp directory",
			slog.String("dir", cfg.Download.TempDir),
			slog.Any("error", err),
		)
		os.Exit(1)
	}

	// Получаем абсолютный путь к временной директории
	absTempDir, err := filepath.Abs(cfg.Download.TempDir)
	if err != nil {
		logger.Error("Failed to get absolute temp dir path", slog.Any("error", err))
		os.Exit(1)
	}
	cfg.Download.TempDir = absTempDir

	logger.Info("Temp directory created", slog.String("dir", cfg.Download.TempDir))

	// Создание сервиса авторизации
	authService := auth.NewService(logger, cfg.Auth)

	// Создание сервиса загрузки
	downloadService := downloader.NewService(
		logger,
		cfg.Download.TempDir,
		cfg.Download.VideoQuality,
	)

	// Создание бота
	bot, err := telegram.NewBot(
		cfg.Telegram.BotToken,
		logger,
		downloadService,
		authService,
		cfg.Download.MaxVideoSizeMB,
		cfg.Download.WorkerPoolSize,
	)
	if err != nil {
		logger.Error("Failed to create bot", slog.Any("error", err))
		os.Exit(1)
	}

	// Обработка сигналов для graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Запуск бота в отдельной горутине
	go func() {
		if err := bot.Start(); err != nil {
			logger.Error("Bot stopped with error", slog.Any("error", err))
		}
	}()

	logger.Info("Bot is running. Press Ctrl+C to stop.")

	// Ожидание сигнала завершения
	<-sigChan
	logger.Info("Received shutdown signal, stopping bot...")

	bot.Stop()

	logger.Info("Application stopped")
}

// initLogger инициализирует логгер slog и на stdout, и в файл
func initLogger() *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}

	consoleHandler := slog.NewTextHandler(os.Stderr, opts)

	// Путь к лог-файлу можно переопределить через переменную окружения LOG_FILE
	logFilePath := os.Getenv("LOG_FILE")
	if logFilePath == "" {
		logFilePath = "reelser-bot.log"
	}

	var handler slog.Handler = consoleHandler

	if f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fileHandler := slog.NewTextHandler(f, opts)
		handler = &multiHandler{handlers: []slog.Handler{consoleHandler, fileHandler}}
	} else {
		// Если файл открыть не удалось — продолжаем логировать только в консоль
		consoleHandler.Handle(
			context.Background(),
			slog.Record{
				Time:    time.Now(),
				Level:   slog.LevelWarn,
				Message: "Failed to open log file, logging only to stderr",
			},
		)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)

	return logger
}

// multiHandler отправляет записи в несколько хендлеров
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		// Игнорируем ошибки отдельных хендлеров, чтобы не блокировать логирование
		_ = h.Handle(ctx, r)
	}
	return nil
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newHandlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		newHandlers[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: newHandlers}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	newHandlers := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		newHandlers[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: newHandlers}
}
