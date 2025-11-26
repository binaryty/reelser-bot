package main

import (
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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

// initLogger инициализирует логгер slog
func initLogger() *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}

	handler := slog.NewTextHandler(os.Stderr, opts)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	return logger
}
