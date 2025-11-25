package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/reelser-bot/internal/config"
	"github.com/reelser-bot/internal/services/auth"
	"github.com/reelser-bot/internal/services/downloader"
	"github.com/reelser-bot/internal/transport/telegram"

	"go.uber.org/zap"
)

func main() {
	// Инициализация логгера
	logger, err := initLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Info("Starting application...")

	// Загрузка конфигурации
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal("Failed to load configuration", zap.Error(err))
	}

	logger.Info("Configuration loaded successfully")

	// Создание временной директории
	if err := os.MkdirAll(cfg.Download.TempDir, 0755); err != nil {
		logger.Fatal("Failed to create temp directory",
			zap.String("dir", cfg.Download.TempDir),
			zap.Error(err),
		)
	}

	// Получаем абсолютный путь к временной директории
	absTempDir, err := filepath.Abs(cfg.Download.TempDir)
	if err != nil {
		logger.Fatal("Failed to get absolute temp dir path", zap.Error(err))
	}
	cfg.Download.TempDir = absTempDir

	logger.Info("Temp directory created", zap.String("dir", cfg.Download.TempDir))

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
		logger.Fatal("Failed to create bot", zap.Error(err))
	}

	// Обработка сигналов для graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Запуск бота в отдельной горутине
	go func() {
		if err := bot.Start(); err != nil {
			logger.Error("Bot stopped with error", zap.Error(err))
		}
	}()

	logger.Info("Bot is running. Press Ctrl+C to stop.")

	// Ожидание сигнала завершения
	<-sigChan
	logger.Info("Received shutdown signal, stopping bot...")

	bot.Stop()

	logger.Info("Application stopped")
}

// initLogger инициализирует логгер zap
func initLogger() (*zap.Logger, error) {
	// Для простоты используем development конфигурацию
	// В production можно использовать production конфигурацию
	config := zap.NewDevelopmentConfig()
	// Development конфигурация уже включает цветное логирование по умолчанию

	logger, err := config.Build()
	if err != nil {
		return nil, fmt.Errorf("failed to build logger: %w", err)
	}

	return logger, nil
}
