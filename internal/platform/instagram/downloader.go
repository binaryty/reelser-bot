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

	"github.com/reelser-bot/internal/common"
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

	if err := d.validateYTDLP(); err != nil {
		return nil, err
	}

	mediaType := d.determineMediaTypeForURL(ctx, url)

	latestFile, err := d.downloadMedia(ctx, url, mediaType)
	if err != nil {
		return nil, err
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

// validateYTDLP проверяет наличие yt-dlp
func (d *Downloader) validateYTDLP() error {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		d.logger.Error("yt-dlp not found")
		return fmt.Errorf("yt-dlp not found. Please install yt-dlp: https://github.com/yt-dlp/yt-dlp")
	}
	return nil
}

// determineMediaTypeForURL определяет тип медиа по URL
func (d *Downloader) determineMediaTypeForURL(ctx context.Context, url string) MediaType {
	// Для Reels всегда видео
	if IsReelURL(url) {
		d.logger.Info("URL is a Reel, forcing video type", slog.String("url", url))
		return MediaTypeVideo
	}

	// Для остальных пытаемся определить тип
	detectedType, err := d.detectMediaType(ctx, url)
	if err != nil {
		d.logger.Warn("Failed to detect media type, defaulting to video",
			slog.String("url", url),
			slog.Any("error", err),
		)
		return MediaTypeVideo
	}

	d.logger.Info("Detected media type", slog.String("type", string(detectedType)), slog.String("url", url))
	return detectedType
}

// downloadMedia скачивает медиа и возвращает путь к файлу
func (d *Downloader) downloadMedia(ctx context.Context, url string, mediaType MediaType) (string, error) {
	outputFile := filepath.Join(d.tempDir, "ig_%(title)s.%(ext)s")
	args := d.buildDownloadArgs(url, outputFile, mediaType)

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	cmd.Dir = d.tempDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", d.handleDownloadError(url, mediaType, output, err)
	}

	return d.findLatestFile()
}

// buildDownloadArgs формирует аргументы для yt-dlp
func (d *Downloader) buildDownloadArgs(url, outputFile string, mediaType MediaType) []string {
	args := []string{
		url,
		"-o", outputFile,
		"--no-playlist",
		"--no-warnings",
		"--quiet",
	}

	switch mediaType {
	case MediaTypeVideo:
		args = append(args, "-f", d.getFormatString())
	case MediaTypePhoto:
		args = append(args, "-f", common.BestFormatExtMp4)
	case MediaTypeAudio:
		args = append(args, "-f", "bestaudio/best", "-x", "--audio-format", "mp3")
	}

	return args
}

// handleDownloadError обрабатывает ошибки скачивания
func (d *Downloader) handleDownloadError(url string, mediaType MediaType, output []byte, err error) error {
	d.logger.Error("Failed to download Instagram media",
		slog.String("url", url),
		slog.String("type", string(mediaType)),
		slog.Any("error", err),
		slog.String("output", string(output)),
	)

	outputStr := string(output)
	if strings.Contains(outputStr, "empty media response") ||
		strings.Contains(outputStr, "being logged-in") ||
		strings.Contains(outputStr, "cookies") {
		return fmt.Errorf(
			"❌ Этот пост недоступен без авторизации в Instagram.\n\n" +
				"Попробуйте:\n" +
				"1. Открыть ссылку в браузере где вы залогинены в Instagram\n" +
				"2. Или используйте другой пост\n" +
				"3. Некоторые посты могут быть приватными или удалены")
	}

	return fmt.Errorf("failed to download media: %w", err)
}

// findLatestFile находит самый новый скачанный файл
func (d *Downloader) findLatestFile() (string, error) {
	files, err := filepath.Glob(filepath.Join(d.tempDir, "ig_*"))
	if err != nil {
		d.logger.Error("Failed to glob files", slog.String("pattern", "ig_*"), slog.Any("error", err))
		return "", fmt.Errorf("failed to find downloaded file: %w", err)
	}

	if len(files) == 0 {
		d.logger.Error("No downloaded files found")
		return "", fmt.Errorf("downloaded file not found")
	}

	var latestFile string
	var latestTime int64
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			d.logger.Warn("Failed to stat file", slog.String("file", file), slog.Any("error", err))
			continue
		}
		if info.ModTime().Unix() > latestTime {
			latestTime = info.ModTime().Unix()
			latestFile = file
		}
	}

	if latestFile == "" {
		d.logger.Error("Could not determine latest file")
		return "", fmt.Errorf("downloaded file not found")
	}

	d.logger.Debug("Found latest file", slog.String("file", latestFile))
	return latestFile, nil
}

// mediaInfo представляет информацию о медиа из yt-dlp
type mediaInfo struct {
	Entries []struct {
		Ext    string `json:"ext"`
		Vcodec string `json:"vcodec"`
		Acodec string `json:"acodec"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"entries"`
	Ext    string `json:"ext"`
	Vcodec string `json:"vcodec"`
	Acodec string `json:"acodec"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// detectMediaType определяет тип медиа через yt-dlp
func (d *Downloader) detectMediaType(ctx context.Context, url string) (MediaType, error) {
	output, err := d.fetchMediaInfo(ctx, url)
	if err != nil {
		return MediaTypeVideo, fmt.Errorf("failed to fetch media info: %w", err)
	}

	info, err := d.parseMediaInfo(output)
	if err != nil {
		return d.detectTypeFromOutput(string(output))
	}

	entry := d.extractEntry(info)
	return d.determineMediaType(entry), nil
}

// fetchMediaInfo получает информацию о медиа через yt-dlp
func (d *Downloader) fetchMediaInfo(ctx context.Context, url string) ([]byte, error) {
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
		d.logger.Error("yt-dlp failed to get media info",
			slog.String("url", url),
			slog.String("output", string(output)),
			slog.Any("error", err))
		return nil, err
	}

	return output, nil
}

// parseMediaInfo парсит JSON ответ от yt-dlp
func (d *Downloader) parseMediaInfo(output []byte) (*mediaInfo, error) {
	var info mediaInfo
	if err := json.Unmarshal(output, &info); err != nil {
		d.logger.Warn("Failed to parse media info JSON",
			slog.String("output_preview", string(output)[:min(len(output), 200)]),
			slog.Any("error", err))
		return nil, err
	}
	return &info, nil
}

// extractEntry извлекает данные из Entries или корня структуры
func (d *Downloader) extractEntry(info *mediaInfo) *mediaInfo {
	entry := *info
	if len(info.Entries) > 0 {
		entry.Ext = info.Entries[0].Ext
		entry.Vcodec = info.Entries[0].Vcodec
		entry.Acodec = info.Entries[0].Acodec
		entry.Width = info.Entries[0].Width
		entry.Height = info.Entries[0].Height
	}
	return &entry
}

// determineMediaType определяет тип медиа по кодекам и размерам
func (d *Downloader) determineMediaType(entry *mediaInfo) MediaType {
	// Если есть видеокодек - это видео
	if entry.Vcodec != common.NoneConst && entry.Vcodec != "" {
		d.logger.Debug("Detected video by vcodec",
			slog.String("vcodec", entry.Vcodec))
		return MediaTypeVideo
	}

	// Если есть только аудиокодек - это аудио
	if entry.Acodec != common.NoneConst && entry.Acodec != "" {
		d.logger.Debug("Detected audio by acodec",
			slog.String("acodec", entry.Acodec))
		return MediaTypeAudio
	}

	// Если есть размеры (ширина/высота) - это фото
	if entry.Width > 0 && entry.Height > 0 {
		d.logger.Debug("Detected photo by dimensions",
			slog.Int("width", entry.Width),
			slog.Int("height", entry.Height))
		return MediaTypePhoto
	}

	// По расширению файла
	ext := strings.ToLower(entry.Ext)
	switch ext {
	case "jpg", "jpeg", "png", "webp":
		d.logger.Debug("Detected photo by extension", slog.String("ext", ext))
		return MediaTypePhoto
	case "mp3", "m4a", "ogg", "opus":
		d.logger.Debug("Detected audio by extension", slog.String("ext", ext))
		return MediaTypeAudio
	}

	d.logger.Debug("Defaulting to video type")
	return MediaTypeVideo
}

// detectTypeFromOutput определяет тип по текстовому выводу (fallback)
func (d *Downloader) detectTypeFromOutput(output string) (MediaType, error) {
	if strings.Contains(output, "video") || strings.Contains(output, "mp4") {
		d.logger.Debug("Detected video from output text")
		return MediaTypeVideo, nil
	}
	if strings.Contains(output, "image") || strings.Contains(output, "jpg") || strings.Contains(output, "png") {
		d.logger.Debug("Detected photo from output text")
		return MediaTypePhoto, nil
	}
	d.logger.Warn("Could not detect media type from output, defaulting to video")
	return MediaTypeVideo, fmt.Errorf("could not detect media type from output")
}

// getFormatString возвращает строку формата для yt-dlp
func (d *Downloader) getFormatString() string {
	switch strings.ToLower(d.videoQuality) {
	case "best":
		return common.BestFormatExtMp4
	case "worst":
		return "worst[ext=mp4]/worst"
	default:
		return common.BestFormatExtMp4
	}
}

// IsReelURL проверяет, является ли URL ссылкой на Reel
func IsReelURL(url string) bool {
	lowerURL := strings.ToLower(url)
	return strings.Contains(lowerURL, "/reel/") || strings.Contains(lowerURL, "/reels/")
}

// IsValidURL проверяет, является ли URL валидной ссылкой на Instagram
func IsValidURL(url string) bool {
	return strings.Contains(url, "instagram.com")
}
