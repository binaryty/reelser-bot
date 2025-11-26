package downloader

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/reelser-bot/internal/platform/instagram"
	"github.com/reelser-bot/internal/platform/tiktok"
	"github.com/reelser-bot/internal/platform/yt"
)

// VideoDownloader интерфейс для загрузки видео
type VideoDownloader interface {
	Download(ctx context.Context, url string) (string, error) // путь к файлу
}

// Service управляет загрузкой видео с разных платформ
type Service struct {
	logger           *slog.Logger
	tempDir          string
	ytDownloader     *yt.Downloader
	tiktokDownloader *tiktok.Downloader
	igDownloader     *instagram.Downloader
}

// NewService создает новый сервис загрузки видео
func NewService(
	logger *slog.Logger,
	tempDir string,
	videoQuality string,
) *Service {
	return &Service{
		logger:           logger,
		tempDir:          tempDir,
		ytDownloader:     yt.NewDownloader(logger, tempDir, videoQuality),
		tiktokDownloader: tiktok.NewDownloader(logger, tempDir),
		igDownloader:     instagram.NewDownloader(logger, tempDir, videoQuality),
	}
}

// Download определяет платформу по URL и скачивает видео
func (s *Service) Download(ctx context.Context, url string) (string, error) {
	s.logger.Info("Processing download request", slog.String("url", url))

	// Определяем платформу
	platform, downloader := s.getDownloader(url)
	if downloader == nil {
		return "", fmt.Errorf("unsupported platform or invalid URL: %s", url)
	}

	s.logger.Info("Platform detected", slog.String("platform", platform))

	// Скачиваем видео
	filePath, err := downloader.Download(ctx, url)
	if err != nil {
		s.logger.Error("Failed to download video",
			slog.String("url", url),
			slog.String("platform", platform),
			slog.Any("error", err),
		)
		return "", fmt.Errorf("failed to download video: %w", err)
	}

	// Проверяем существование файла
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return "", fmt.Errorf("downloaded file does not exist: %s", filePath)
	}

	s.logger.Info("Video downloaded successfully",
		slog.String("url", url),
		slog.String("platform", platform),
		slog.String("file", filePath),
	)

	return filePath, nil
}

// getDownloader возвращает соответствующий загрузчик для URL
func (s *Service) getDownloader(url string) (string, VideoDownloader) {
	urlLower := strings.ToLower(url)

	if yt.IsValidURL(urlLower) {
		return "youtube", s.ytDownloader
	}

	if tiktok.IsValidURL(urlLower) {
		return "tiktok", s.tiktokDownloader
	}

	if instagram.IsValidURL(urlLower) {
		return "instagram", s.igDownloader
	}

	return "unknown", nil
}

// Cleanup удаляет временный файл
func (s *Service) Cleanup(filePath string) error {
	if filePath == "" {
		return nil
	}

	// Проверяем, что файл находится в tempDir для безопасности
	absTempDir, err := filepath.Abs(s.tempDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute temp dir: %w", err)
	}

	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute file path: %w", err)
	}

	if !strings.HasPrefix(absFilePath, absTempDir) {
		return fmt.Errorf("file path is outside temp directory")
	}

	if err := os.Remove(filePath); err != nil {
		s.logger.Warn("Failed to remove temporary file",
			slog.String("file", filePath),
			slog.Any("error", err),
		)
		return err
	}

	s.logger.Info("Temporary file removed", slog.String("file", filePath))
	return nil
}

// GetFileSize возвращает размер файла в байтах
func (s *Service) GetFileSize(filePath string) (int64, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
