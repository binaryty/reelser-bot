package instagram

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Downloader реализует загрузку видео с Instagram
type Downloader struct {
	logger       *slog.Logger
	tempDir      string
	videoQuality string
}

// NewDownloader создает новый экземпляр Instagram загрузчика
func NewDownloader(logger *slog.Logger, tempDir, videoQuality string) *Downloader {
	return &Downloader{
		logger:       logger,
		tempDir:      tempDir,
		videoQuality: videoQuality,
	}
}

// Download скачивает видео с Instagram используя yt-dlp
// Возвращает путь к скачанному файлу
func (d *Downloader) Download(ctx context.Context, url string) (string, error) {
	d.logger.Info("Starting Instagram video download", slog.String("url", url))

	// Проверяем наличие yt-dlp
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		return "", fmt.Errorf("yt-dlp not found. Please install yt-dlp: https://github.com/yt-dlp/yt-dlp")
	}

	// Создаем временный файл для сохранения видео
	outputFile := filepath.Join(d.tempDir, "ig_%(title)s.%(ext)s")

	// Формируем команду yt-dlp
	args := []string{
		url,
		"-o", outputFile,
		"-f", d.getFormatString(),
		"--no-playlist",
		"--no-warnings",
		"--quiet",
	}

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	cmd.Dir = d.tempDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		d.logger.Error("Failed to download Instagram video",
			slog.String("url", url),
			slog.Any("error", err),
			slog.String("output", string(output)),
		)
		return "", fmt.Errorf("failed to download video: %w", err)
	}

	// Находим скачанный файл
	files, err := filepath.Glob(filepath.Join(d.tempDir, "ig_*"))
	if err != nil {
		return "", fmt.Errorf("failed to find downloaded file: %w", err)
	}

	if len(files) == 0 {
		return "", fmt.Errorf("downloaded file not found")
	}

	// Находим самый новый файл
	var latestFile string
	var latestTime int64
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}
		if info.ModTime().Unix() > latestTime {
			latestTime = info.ModTime().Unix()
			latestFile = file
		}
	}

	if latestFile == "" {
		return "", fmt.Errorf("downloaded file not found")
	}

	d.logger.Info("Instagram video downloaded successfully",
		slog.String("url", url),
		slog.String("file", latestFile),
	)

	return latestFile, nil
}

// getFormatString возвращает строку формата для yt-dlp
func (d *Downloader) getFormatString() string {
	switch strings.ToLower(d.videoQuality) {
	case "best":
		return "best[ext=mp4]/best"
	case "worst":
		return "worst[ext=mp4]/worst"
	default:
		return "best[ext=mp4]/best"
	}
}

// IsValidURL проверяет, является ли URL валидной ссылкой на Instagram
func IsValidURL(url string) bool {
	return strings.Contains(url, "instagram.com")
}

