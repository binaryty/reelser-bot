package yt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Downloader реализует загрузку видео с YouTube
type Downloader struct {
	logger       *slog.Logger
	tempDir      string
	videoQuality string
	client       *http.Client
}

// NewDownloader создает новый экземпляр YouTube загрузчика
func NewDownloader(logger *slog.Logger, tempDir, videoQuality string) *Downloader {
	return &Downloader{
		logger:       logger,
		tempDir:      tempDir,
		videoQuality: videoQuality,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

const maxFileSizeMB = 50

// Download скачивает видео с YouTube используя yt-dlp с retry logic
// и fallback на SaveFrom API если yt-dlp не справляется
// Автоматически выбирает качество чтобы файл был < 50MB
// Возвращает путь к скачанному файлу
func (d *Downloader) Download(ctx context.Context, videoURL string) (string, error) {
	d.logger.Info("Starting YouTube video download", slog.String("url", videoURL))

	// Проверяем наличие yt-dlp
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		d.logger.Warn("yt-dlp not found, trying SaveFrom fallback",
			slog.String("url", videoURL),
		)
		return d.downloadViaSaveFrom(ctx, videoURL)
	}

	// Получаем информацию о видео и выбираем подходящий формат
	format, err := d.selectFormatBySize(ctx, videoURL)
	if err != nil {
		d.logger.Warn("Could not determine video size, using default format",
			slog.String("url", videoURL),
			slog.Any("error", err),
		)
		format = d.getFormatString()
	}

	d.logger.Info("Selected format", slog.String("format", format), slog.String("url", videoURL))

	// Пробуем скачать с yt-dlp и retry logic
	file, err := d.downloadWithRetry(ctx, videoURL, format)
	if err == nil {
		// Проверяем размер файла
		if info, err := os.Stat(file); err == nil {
			sizeMB := float64(info.Size()) / (1024 * 1024)
			d.logger.Info("Downloaded file size", slog.Float64("size_mb", sizeMB), slog.String("file", file))
		}
		return file, nil
	}

	d.logger.Warn("yt-dlp failed, trying SaveFrom fallback",
		slog.String("url", videoURL),
		slog.Any("error", err),
	)

	// Fallback на SaveFrom
	return d.downloadViaSaveFrom(ctx, videoURL)
}

// downloadWithRetry пытается скачать видео с retry logic
func (d *Downloader) downloadWithRetry(ctx context.Context, videoURL string, format string) (string, error) {
	var lastErr error

	for attempt := 1; attempt <= 3; attempt++ {
		d.logger.Info("Download attempt",
			slog.Int("attempt", attempt),
			slog.String("url", videoURL),
		)

		file, err := d.tryDownload(ctx, videoURL, attempt, format)
		if err == nil {
			return file, nil
		}

		lastErr = err

		// Если это не последняя попытка, ждём перед следующей
		if attempt < 3 {
			sleepTime := time.Duration(attempt*3) * time.Second
			d.logger.Info("Waiting before retry",
				slog.Duration("sleep", sleepTime),
			)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(sleepTime):
			}
		}
	}

	return "", fmt.Errorf("all yt-dlp attempts failed: %w", lastErr)
}

// tryDownload выполняет одну попытку скачивания с разными параметрами
func (d *Downloader) tryDownload(ctx context.Context, videoURL string, attempt int, format string) (string, error) {
	// Создаем временный файл для сохранения видео
	outputFile := filepath.Join(d.tempDir, "yt_%(title)s.%(ext)s")

	// Формируем команду yt-dlp с разными параметрами для каждой попытки
	args := d.buildArgs(videoURL, outputFile, attempt, format)

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	cmd.Dir = d.tempDir

	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		d.logger.Error("Download attempt failed",
			slog.Int("attempt", attempt),
			slog.String("url", videoURL),
			slog.Any("error", err),
			slog.String("output", truncateString(outputStr, 2000)),
		)
		return "", fmt.Errorf("attempt %d failed: %w. Output: %s", attempt, err, truncateString(outputStr, 1000))
	}

	// Находим скачанный файл
	return d.findDownloadedFile(videoURL)
}

// buildArgs формирует аргументы для yt-dlp в зависимости от попытки
func (d *Downloader) buildArgs(videoURL, outputFile string, attempt int, format string) []string {
	args := []string{
		videoURL,
		"-o", outputFile,
		"--no-playlist",
		"--no-warnings",
		"--newline",
	}

	switch attempt {
	case 1:
		// Первая попытка - используем выбранный формат
		args = append(args, "-f", format)

	case 2:
		// Вторая попытка - с задержкой и специфичными флагами для YouTube
		// Используем более низкое качество
		args = append(args,
			"-f", "worst[ext=mp4]/worst",
			"--extractor-args", "youtube:player_client=web",
			"--extractor-args", "youtube:player_skip=webpage,configs,js",
			"--sleep-requests", "2",
			"--add-header", "Accept-Language:en-US,en;q=0.9",
			"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
		)

	case 3:
		// Третья попытка - упрощенный формат и мобильный клиент
		args = append(args,
			"-f", "worst[ext=mp4]/worst",
			"--extractor-args", "youtube:player_client=android",
			"--user-agent", "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36",
			"--geo-bypass",
			"--extractor-args", "youtube:player_skip=webpage,configs,js,player_response",
		)
	}

	return args
}

// findDownloadedFile находит скачанный файл в temp директории
func (d *Downloader) findDownloadedFile(videoURL string) (string, error) {
	files, err := filepath.Glob(filepath.Join(d.tempDir, "yt_*"))
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

	d.logger.Info("YouTube video downloaded successfully",
		slog.String("url", videoURL),
		slog.String("file", latestFile),
	)

	return latestFile, nil
}

// downloadViaSaveFrom скачивает видео через SaveFrom API
func (d *Downloader) downloadViaSaveFrom(ctx context.Context, videoURL string) (string, error) {
	d.logger.Info("Trying SaveFrom fallback", slog.String("url", videoURL))

	// Получаем прямую ссылку на видео через SaveFrom API
	downloadURL, err := d.getSaveFromURL(ctx, videoURL)
	if err != nil {
		return "", fmt.Errorf("savefrom failed: %w", err)
	}

	// Скачиваем видео по прямой ссылке
	return d.downloadFromURL(ctx, downloadURL, videoURL)
}

// getSaveFromURL получает прямую ссылку на видео через SaveFrom API
func (d *Downloader) getSaveFromURL(ctx context.Context, videoURL string) (string, error) {
	// SaveFrom API endpoint
	// Формируем запрос к SaveFrom API
	apiURL := fmt.Sprintf("https://savefrom.net/save-from.php?url=%s", url.QueryEscape(videoURL))

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", "https://savefrom.net/")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch from savefrom: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("savefrom returned status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Пытаемся извлечь URL из JSON ответа
	bodyStr := string(body)

	// Ищем ссылку в разных форматах ответа
	// Формат 1: JSON с полем url
	var jsonResponse struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &jsonResponse); err == nil && jsonResponse.URL != "" {
		return jsonResponse.URL, nil
	}

	// Формат 2: Ищем ссылку на видео в HTML/тексте
	// Ищем ссылки на video/mp4
	videoURLPattern := regexp.MustCompile(`(https?://[^"\s]+\.mp4[^"\s]*)`)
	matches := videoURLPattern.FindStringSubmatch(bodyStr)
	if len(matches) > 0 {
		return matches[1], nil
	}

	// Ищем в JavaScript объектах
	jsURLPattern := regexp.MustCompile(`"(https?://[^"]+)"`)
	jsMatches := jsURLPattern.FindAllStringSubmatch(bodyStr, -1)
	for _, match := range jsMatches {
		if len(match) > 1 && strings.Contains(match[1], "googlevideo.com") {
			return match[1], nil
		}
	}

	return "", fmt.Errorf("could not find video URL in savefrom response")
}

// downloadFromURL скачивает файл по прямой ссылке
func (d *Downloader) downloadFromURL(ctx context.Context, downloadURL, originalURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create download request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://www.youtube.com/")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to download video: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned status: %d", resp.StatusCode)
	}

	// Создаем временный файл
	outputFile := filepath.Join(d.tempDir, fmt.Sprintf("yt_savefrom_%d.mp4", time.Now().Unix()))

	file, err := os.Create(outputFile)
	if err != nil {
		return "", fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	// Копируем данные
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		os.Remove(outputFile)
		return "", fmt.Errorf("failed to save video: %w", err)
	}

	d.logger.Info("Video downloaded via SaveFrom",
		slog.String("url", originalURL),
		slog.String("file", outputFile),
	)

	return outputFile, nil
}

// getFormatString возвращает строку формата для yt-dlp в зависимости от качества
func (d *Downloader) getFormatString() string {
	switch strings.ToLower(d.videoQuality) {
	case "best":
		return "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best"
	case "worst":
		return "worst[ext=mp4]/worst"
	default:
		return "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best"
	}
}

// selectFormatBySize выбирает формат видео исходя из ограничения на размер (50MB)
func (d *Downloader) selectFormatBySize(ctx context.Context, videoURL string) (string, error) {
	// Получаем информацию о форматах видео
	args := []string{
		videoURL,
		"-J", // JSON output
		"--no-playlist",
		"--no-warnings",
		"--quiet",
	}

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get video info: %w", err)
	}

	var videoInfo struct {
		Duration float64 `json:"duration"`
		Formats  []struct {
			FormatID   string  `json:"format_id"`
			Ext        string  `json:"ext"`
			Resolution string  `json:"resolution"`
			Filesize   int64   `json:"filesize"`
			FilesizeApprox float64 `json:"filesize_approx"`
			Width      int     `json:"width"`
			Height     int     `json:"height"`
			Vcodec     string  `json:"vcodec"`
			Acodec     string  `json:"acodec"`
		} `json:"formats"`
	}

	if err := json.Unmarshal(output, &videoInfo); err != nil {
		return "", fmt.Errorf("failed to parse video info: %w", err)
	}

	// Если видео короткое (< 5 минут), пробуем скачать в хорошем качестве
	// Иначе выбираем формат поменьше
	maxSizeBytes := int64(maxFileSizeMB * 1024 * 1024)
	
	// Фильтруем только mp4 форматы с видео
	var suitableFormats []struct {
		FormatID   string
		Resolution string
		Filesize   int64
		Width      int
		Height     int
	}
	
	for _, f := range videoInfo.Formats {
		// Пропускаем аудио-only форматы
		if f.Vcodec == "none" || f.Vcodec == "" {
			continue
		}
		// Берем только mp4
		if f.Ext != "mp4" && f.Ext != "" {
			continue
		}
		
		size := f.Filesize
		if size == 0 && f.FilesizeApprox > 0 {
			size = int64(f.FilesizeApprox)
		}
		
		suitableFormats = append(suitableFormats, struct {
			FormatID   string
			Resolution string
			Filesize   int64
			Width      int
			Height     int
		}{
			FormatID:   f.FormatID,
			Resolution: f.Resolution,
			Filesize:   size,
			Width:      f.Width,
			Height:     f.Height,
		})
	}
	
	if len(suitableFormats) == 0 {
		return "best[ext=mp4]/best", nil
	}
	
	// Сортируем по качеству (разрешению) от высокого к низкому
	for i := 0; i < len(suitableFormats)-1; i++ {
		for j := i + 1; j < len(suitableFormats); j++ {
			if suitableFormats[i].Height < suitableFormats[j].Height {
				suitableFormats[i], suitableFormats[j] = suitableFormats[j], suitableFormats[i]
			}
		}
	}
	
	// Для длинных видео (> 10 минут) сразу выбираем низкое качество
	if videoInfo.Duration > 600 {
		d.logger.Info("Long video detected, selecting low quality", 
			slog.Float64("duration", videoInfo.Duration),
		)
		return "worst[ext=mp4]/worst", nil
	}
	
	// Ищем формат с размером < 50MB
	for _, f := range suitableFormats {
		if f.Filesize > 0 && f.Filesize < maxSizeBytes {
			d.logger.Info("Selected format by size",
				slog.String("format_id", f.FormatID),
				slog.String("resolution", f.Resolution),
				slog.Int64("filesize", f.Filesize),
			)
			return f.FormatID, nil
		}
	}
	
	// Если не нашли подходящий по размеру, выбираем по разрешению
	// Для видео > 5 минут - максимум 720p
	// Для видео < 5 минут - максимум 1080p
	maxHeight := 1080
	if videoInfo.Duration > 300 {
		maxHeight = 720
	}
	
	for _, f := range suitableFormats {
		if f.Height > 0 && f.Height <= maxHeight {
			d.logger.Info("Selected format by resolution",
				slog.String("format_id", f.FormatID),
				slog.String("resolution", f.Resolution),
				slog.Int("height", f.Height),
			)
			return f.FormatID, nil
		}
	}
	
	// Fallback на худшее качество
	return "worst[ext=mp4]/worst", nil
}

// truncateString обрезает строку до указанной длины
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// IsValidURL проверяет, является ли URL валидной ссылкой на YouTube
func IsValidURL(url string) bool {
	lowerURL := strings.ToLower(url)
	return strings.Contains(lowerURL, "youtube.com") ||
		strings.Contains(lowerURL, "youtu.be")
}

// IsShorts проверяет, является ли URL YouTube Shorts
func IsShorts(url string) bool {
	return strings.Contains(strings.ToLower(url), "/shorts/")
}

// GetVideoID извлекает ID видео из URL
func GetVideoID(videoURL string) string {
	patterns := []string{
		`[?&]v=([^&]+)`,
		`youtu\.be/([^?&]+)`,
		`/shorts/([^?&]+)`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(videoURL)
		if len(matches) > 1 {
			return matches[1]
		}
	}

	return ""
}
