package instagram

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MediaType представляет тип медиа
type MediaType string

const (
	MediaTypeVideo MediaType = "video"
	MediaTypePhoto MediaType = "photo"
	MediaTypeAudio MediaType = "audio"
)

// DownloadResult содержит результат загрузки
type DownloadResult struct {
	FilePath string
	Type     MediaType
}

// Downloader реализует загрузку медиа с Instagram
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

// Download скачивает медиа с Instagram используя yt-dlp
// Возвращает путь к скачанному файлу и тип медиа
func (d *Downloader) Download(ctx context.Context, url string) (string, error) {
	result, err := d.DownloadWithType(ctx, url)
	if err != nil {
		return "", err
	}
	return result.FilePath, nil
}

// DownloadWithType скачивает медиа с Instagram и определяет его тип
func (d *Downloader) DownloadWithType(ctx context.Context, url string) (*DownloadResult, error) {
	d.logger.Info("Starting Instagram media download", slog.String("url", url))

	// Проверяем наличие yt-dlp
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		return nil, fmt.Errorf("yt-dlp not found. Please install yt-dlp: https://github.com/yt-dlp/yt-dlp")
	}

	// Сначала получаем информацию о медиа для определения типа
	mediaType, err := d.detectMediaType(ctx, url)
	if err != nil {
		d.logger.Warn("Failed to detect media type, defaulting to video",
			slog.String("url", url),
			slog.Any("error", err),
		)
		mediaType = MediaTypeVideo
	}

	d.logger.Info("Detected media type", slog.String("type", string(mediaType)), slog.String("url", url))

	// Создаем временный файл для сохранения медиа
	outputFile := filepath.Join(d.tempDir, "ig_%(title)s.%(ext)s")

	// Формируем команду yt-dlp в зависимости от типа медиа
	args := []string{
		url,
		"-o", outputFile,
		"--no-playlist",
		"--no-warnings",
		"--quiet",
	}

	// Добавляем формат в зависимости от типа медиа
	switch mediaType {
	case MediaTypeVideo:
		args = append(args, "-f", d.getFormatString())
	case MediaTypePhoto:
		// Для фото скачиваем лучшее качество
		args = append(args, "-f", "best")
	case MediaTypeAudio:
		// Для аудио скачиваем только аудио
		args = append(args, "-f", "bestaudio/best", "-x", "--audio-format", "mp3")
	}

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	cmd.Dir = d.tempDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		d.logger.Error("Failed to download Instagram media",
			slog.String("url", url),
			slog.String("type", string(mediaType)),
			slog.Any("error", err),
			slog.String("output", string(output)),
		)
		return nil, fmt.Errorf("failed to download media: %w", err)
	}

	// Находим скачанный файл
	files, err := filepath.Glob(filepath.Join(d.tempDir, "ig_*"))
	if err != nil {
		return nil, fmt.Errorf("failed to find downloaded file: %w", err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("downloaded file not found")
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
		return nil, fmt.Errorf("downloaded file not found")
	}

	d.logger.Info("Instagram media downloaded successfully",
		slog.String("url", url),
		slog.String("file", latestFile),
		slog.String("type", string(mediaType)),
	)

	return &DownloadResult{
		FilePath: latestFile,
		Type:     mediaType,
	}, nil
}

// detectMediaType определяет тип медиа через yt-dlp
func (d *Downloader) detectMediaType(ctx context.Context, url string) (MediaType, error) {
	args := []string{
		url,
		"-J", // JSON output
		"--no-playlist",
		"--no-warnings",
		"--quiet",
	}

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return MediaTypeVideo, fmt.Errorf("failed to get media info: %w", err)
	}

	var info struct {
		Entries []struct {
			Ext      string `json:"ext"`
			Vcodec   string `json:"vcodec"`
			Acodec   string `json:"acodec"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
		} `json:"entries"`
		Ext      string `json:"ext"`
		Vcodec   string `json:"vcodec"`
		Acodec   string `json:"acodec"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
	}

	if err := json.Unmarshal(output, &info); err != nil {
		// Если это не JSON (может быть список), пробуем определить по расширению
		outputStr := string(output)
		if strings.Contains(outputStr, "video") || strings.Contains(outputStr, "mp4") {
			return MediaTypeVideo, nil
		}
		if strings.Contains(outputStr, "image") || strings.Contains(outputStr, "jpg") || strings.Contains(outputStr, "png") {
			return MediaTypePhoto, nil
		}
		return MediaTypeVideo, fmt.Errorf("failed to parse media info: %w", err)
	}

	// Определяем тип по информации о медиа
	entry := info
	if len(info.Entries) > 0 {
		entry.Ext = info.Entries[0].Ext
		entry.Vcodec = info.Entries[0].Vcodec
		entry.Acodec = info.Entries[0].Acodec
		entry.Width = info.Entries[0].Width
		entry.Height = info.Entries[0].Height
	}

	// Если есть видеокодек - это видео
	if entry.Vcodec != "none" && entry.Vcodec != "" {
		return MediaTypeVideo, nil
	}

	// Если есть только аудиокодек - это аудио
	if entry.Acodec != "none" && entry.Acodec != "" && (entry.Vcodec == "none" || entry.Vcodec == "") {
		return MediaTypeAudio, nil
	}

	// Если есть размеры (ширина/высота) но нет видеокодека - это фото
	if entry.Width > 0 && entry.Height > 0 && (entry.Vcodec == "none" || entry.Vcodec == "") {
		return MediaTypePhoto, nil
	}

	// По расширению файла
	ext := strings.ToLower(entry.Ext)
	if ext == "jpg" || ext == "jpeg" || ext == "png" || ext == "webp" {
		return MediaTypePhoto, nil
	}
	if ext == "mp3" || ext == "m4a" || ext == "ogg" || ext == "opus" {
		return MediaTypeAudio, nil
	}

	// По умолчанию считаем видео
	return MediaTypeVideo, nil
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

