package tiktok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Downloader реализует загрузку видео с TikTok
type Downloader struct {
	logger  *slog.Logger
	tempDir string
	client  *http.Client
}

// NewDownloader создает новый экземпляр TikTok загрузчика
func NewDownloader(logger *slog.Logger, tempDir string) *Downloader {
	return &Downloader{
		logger:  logger,
		tempDir: tempDir,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Download скачивает видео с TikTok используя TikWM API
// Возвращает путь к скачанному файлу
func (d *Downloader) Download(ctx context.Context, url string) (string, error) {
	d.logger.Info("Starting TikTok video download", slog.String("url", url))

	// Используем TikWM API для получения прямой ссылки на видео
	apiURL := fmt.Sprintf("https://tikwm.com/api?url=%s", url)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := d.client.Do(req)
	if err != nil {
		d.logger.Error("Failed to fetch TikTok video info",
			slog.String("url", url),
			slog.Any("error", err),
		)
		return "", fmt.Errorf("failed to fetch video info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API returned status code: %d", resp.StatusCode)
	}

	// Парсим JSON ответ
	var apiResponse struct {
		Code int `json:"code"`
		Data struct {
			Play string `json:"play"`
		} `json:"data"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Парсим JSON ответ
	if err := json.Unmarshal(body, &apiResponse); err != nil {
		// Если не удалось распарсить JSON, пробуем извлечь URL вручную
		playURL := extractPlayURL(string(body))
		if playURL == "" {
			return "", fmt.Errorf("failed to parse API response: %w", err)
		}
		apiResponse.Data.Play = playURL
	}

	if apiResponse.Data.Play == "" {
		return "", fmt.Errorf("video URL not found in API response")
	}

	playURL := apiResponse.Data.Play

	// Скачиваем видео
	videoReq, err := http.NewRequestWithContext(ctx, "GET", playURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create video request: %w", err)
	}

	videoReq.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	videoReq.Header.Set("Referer", "https://www.tiktok.com/")

	videoResp, err := d.client.Do(videoReq)
	if err != nil {
		return "", fmt.Errorf("failed to download video: %w", err)
	}
	defer videoResp.Body.Close()

	if videoResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("video download returned status code: %d", videoResp.StatusCode)
	}

	// Создаем временный файл
	outputFile := filepath.Join(d.tempDir, fmt.Sprintf("tiktok_%d.mp4", time.Now().Unix()))

	file, err := os.Create(outputFile)
	if err != nil {
		return "", fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	// Копируем данные
	_, err = io.Copy(file, videoResp.Body)
	if err != nil {
		os.Remove(outputFile)
		return "", fmt.Errorf("failed to save video: %w", err)
	}

	d.logger.Info("TikTok video downloaded successfully",
		slog.String("url", url),
		slog.String("file", outputFile),
	)

	return outputFile, nil
}

// extractPlayURL извлекает URL видео из JSON ответа API
func extractPlayURL(jsonStr string) string {
	// Простой поиск URL в JSON (можно улучшить используя encoding/json)
	start := strings.Index(jsonStr, `"play":"`)
	if start == -1 {
		return ""
	}
	start += 8 // длина `"play":`

	end := strings.Index(jsonStr[start:], `"`)
	if end == -1 {
		return ""
	}

	url := jsonStr[start : start+end]
	// Убираем экранированные символы
	url = strings.ReplaceAll(url, "\\/", "/")
	url = strings.ReplaceAll(url, "\\u0026", "&")

	return url
}

// IsValidURL проверяет, является ли URL валидной ссылкой на TikTok
func IsValidURL(url string) bool {
	return strings.Contains(url, "tiktok.com")
}
