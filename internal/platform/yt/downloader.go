package yt

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/reelser-bot/internal/common"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// VideoQuality представляет доступное качество видео
type VideoQuality struct {
	FormatID    string
	Resolution  string // 1080p, 720p, etc.
	Height      int
	Width       int
	Filesize    int64
	Ext         string
	Vcodec      string
	Acodec      string
	IsAudioOnly bool
}

// ProgressCallback функция для обновления прогресса
type ProgressCallback func(percent int, downloaded int64, total int64, speed string, eta string)

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

// Download скачивает видео с YouTube используя yt-dlp
// Возвращает путь к скачанному файлу
func (d *Downloader) Download(ctx context.Context, videoURL string) (string, error) {
	return d.DownloadWithQuality(ctx, videoURL, "best", nil)
}

// DownloadWithQuality скачивает видео с выбранным качеством
// quality: "1080", "720", "480", "audio" или "best"
// progressCallback вызывается для обновления прогресса
func (d *Downloader) DownloadWithQuality(
	ctx context.Context,
	videoURL string,
	quality string,
	progressCallback ProgressCallback,
) (string, error) {
	d.logger.Info("Starting YouTube video download",
		slog.String("url", videoURL),
		slog.String("quality", quality),
	)

	// Проверяем наличие yt-dlp
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		d.logger.Warn("yt-dlp not found, trying SaveFrom fallback",
			slog.String("url", videoURL),
		)
		return d.downloadViaSaveFrom(ctx, videoURL, progressCallback)
	}

	// Формируем формат строку на основе качества
	format := d.qualityToFormat(quality)

	// Скачиваем с прогрессом
	file, err := d.downloadWithProgress(ctx, videoURL, format, progressCallback)
	if err == nil {
		return file, nil
	}

	d.logger.Warn("yt-dlp failed, trying SaveFrom fallback",
		slog.String("url", videoURL),
		slog.Any("error", err),
	)

	// Fallback на SaveFrom
	return d.downloadViaSaveFrom(ctx, videoURL, progressCallback)
}

// qualityToFormat преобразует запрошенное качество в форматную строку yt-dlp
// Используем уже объединенные форматы (содержащие и видео и аудио) чтобы избежать проблем с мержем
func (d *Downloader) qualityToFormat(quality string) string {
	switch quality {
	case "2160":
		// 4K: ищем любой формат 2160p (часто только webm)
		return "best[height=2160]/best[height<=2160]/best"
	case "1440":
		// 2K: ищем любой формат 1440p
		return "best[height=1440]/best[height<=1440]/best"
	case "1080":
		// Сначала пробуем найти уже объединенный формат 1080p, иначе best <=1080
		return "best[height=1080][ext=mp4]/best[height<=1080][ext=mp4]/best[height<=1080]"
	case "720":
		// Сначала пробуем найти уже объединенный формат 720p
		return "best[height=720][ext=mp4]/best[height<=720][ext=mp4]/best[height<=720]"
	case "480":
		// Сначала пробуем найти уже объединенный формат 480p
		return "best[height=480][ext=mp4]/best[height<=480][ext=mp4]/best[height<=480]"
	case "audio":
		return "bestaudio[ext=m4a]/bestaudio/best"
	case "best":
		// Для best пробуем сначала объединенные форматы
		return common.BestFormatExtMp4
	default:
		return common.BestFormatExtMp4
	}
}

// GetAvailableQualities возвращает список доступных качеств для видео
func (d *Downloader) GetAvailableQualities(ctx context.Context, videoURL string) ([]VideoQuality, error) {
	// Получаем информацию о видео
	args := []string{
		videoURL,
		"-J",
		"--no-playlist",
		"--no-warnings",
	}

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get video info: %w", err)
	}

	var videoInfo struct {
		Title    string `json:"title"`
		Duration int    `json:"duration"`
		Formats  []struct {
			FormatID       string  `json:"format_id"`
			Ext            string  `json:"ext"`
			Resolution     string  `json:"resolution"`
			Height         int     `json:"height"`
			Width          int     `json:"width"`
			Filesize       int64   `json:"filesize"`
			FilesizeApprox float64 `json:"filesize_approx"`
			Vcodec         string  `json:"vcodec"`
			Acodec         string  `json:"acodec"`
		} `json:"formats"`
	}

	if err := json.Unmarshal(output, &videoInfo); err != nil {
		return nil, fmt.Errorf("failed to parse video info: %w", err)
	}

	// Группируем форматы по качеству
	qualityMap := make(map[int]*VideoQuality)
	hasAudio := false

	for _, f := range videoInfo.Formats {
		// Пропускаем аудио-only форматы для видео списка
		if f.Vcodec == "none" || f.Vcodec == "" {
			if f.Acodec != "none" && f.Acodec != "" {
				hasAudio = true
			}
			continue
		}

		// Берем mp4 и webm (4K/2K часто только в webm)
		if f.Ext != "mp4" && f.Ext != "webm" && f.Ext != "" {
			continue
		}

		// Округляем высоту до стандартных значений
		height := d.normalizeHeight(f.Height)
		if height == 0 {
			continue
		}

		// Используем наилучший формат для данной высоты
		size := f.Filesize
		if size == 0 && f.FilesizeApprox > 0 {
			size = int64(f.FilesizeApprox)
		}

		if existing, ok := qualityMap[height]; !ok || size > existing.Filesize {
			qualityMap[height] = &VideoQuality{
				FormatID:   f.FormatID,
				Resolution: fmt.Sprintf("%dp", height),
				Height:     height,
				Width:      f.Width,
				Filesize:   size,
				Ext:        f.Ext,
				Vcodec:     f.Vcodec,
				Acodec:     f.Acodec,
			}
		}
	}

	// Преобразуем map в слайс
	var qualities []VideoQuality
	for _, q := range qualityMap {
		qualities = append(qualities, *q)
	}

	// Сортируем по убыванию качества
	for i := 0; i < len(qualities)-1; i++ {
		for j := i + 1; j < len(qualities); j++ {
			if qualities[i].Height < qualities[j].Height {
				qualities[i], qualities[j] = qualities[j], qualities[i]
			}
		}
	}

	// Добавляем опцию audio-only если есть аудио
	if hasAudio {
		qualities = append(qualities, VideoQuality{
			Resolution:  "Audio",
			IsAudioOnly: true,
		})
	}

	return qualities, nil
}

// normalizeHeight округляет высоту до стандартных значений
func (d *Downloader) normalizeHeight(height int) int {
	if height >= 2160 {
		return 2160
	} else if height >= 1440 {
		return 1440
	} else if height >= 1080 {
		return 1080
	} else if height >= 720 {
		return 720
	} else if height >= 480 {
		return 480
	} else if height >= 360 {
		return 360
	}
	return 0
}

// downloadWithProgress скачивает видео с отслеживанием прогресса
func (d *Downloader) downloadWithProgress(
	ctx context.Context,
	videoURL string,
	format string,
	progressCallback ProgressCallback,
) (string, error) {
	outputFile := filepath.Join(d.tempDir, "yt_%(title)s.%(ext)s")

	args := []string{
		videoURL,
		"-o", outputFile,
		"-f", format,
		"--no-playlist",
		"--no-warnings",
		"--newline",
		"--progress",
		"--merge-output-format", "mp4",
		"--remux-video", "mp4",
	}

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	cmd.Dir = d.tempDir

	// Если есть callback, перехватываем вывод для прогресса
	if progressCallback != nil {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return "", fmt.Errorf("failed to create stdout pipe: %w", err)
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			return "", fmt.Errorf("failed to create stderr pipe: %w", err)
		}

		if err := cmd.Start(); err != nil {
			return "", fmt.Errorf("failed to start download: %w", err)
		}

		// Читаем прогресс
		go d.parseProgress(stdout, progressCallback)
		go d.parseProgress(stderr, progressCallback)

		if err := cmd.Wait(); err != nil {
			return "", fmt.Errorf("download failed: %w", err)
		}
	} else {
		// Без прогресса - просто выполняем команду
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("download failed: %w. Output: %s", err, string(output))
		}
	}

	return d.findDownloadedFile(videoURL)
}

// parseProgress парсит вывод yt-dlp и вызывает callback
func (d *Downloader) parseProgress(reader io.Reader, callback ProgressCallback) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()

		// Ищем строки прогресса: [download] 45.2% of ~50.00MiB at 2.5MiB/s ETA 00:15
		if strings.Contains(line, "[download]") && strings.Contains(line, "%") {
			percent, downloaded, total, speed, eta := d.parseProgressLine(line)
			if percent >= 0 {
				callback(percent, downloaded, total, speed, eta)
			}
		}
	}
}

// parseProgressLine парсит строку прогресса
func (d *Downloader) parseProgressLine(line string) (int, int64, int64, string, string) {
	// Пример: [download] 45.2% of ~50.00MiB at 2.5MiB/s ETA 00:15

	// Извлекаем процент
	percentRegex := regexp.MustCompile(`(\d+\.?\d*)%`)
	percentMatch := percentRegex.FindStringSubmatch(line)
	if len(percentMatch) < 2 {
		return -1, 0, 0, "", ""
	}
	percent, err := strconv.ParseFloat(percentMatch[1], 64)
	if err != nil {
		return -1, 0, 0, "", ""
	}

	// Извлекаем размер
	sizeRegex := regexp.MustCompile(`of\s+~?(\d+\.?\d*)([KMGT]i?B)`)
	sizeMatch := sizeRegex.FindStringSubmatch(line)
	total := int64(0)
	if len(sizeMatch) >= 3 {
		sizeVal, err := strconv.ParseFloat(sizeMatch[1], 64)
		if err != nil {
			return -1, 0, 0, "", ""
		}

		unit := sizeMatch[2]
		switch {
		case strings.HasPrefix(unit, "K"):
			total = int64(sizeVal * 1024)
		case strings.HasPrefix(unit, "M"):
			total = int64(sizeVal * 1024 * 1024)
		case strings.HasPrefix(unit, "G"):
			total = int64(sizeVal * 1024 * 1024 * 1024)
		}
	}

	downloaded := int64(float64(total) * percent / 100)

	// Извлекаем скорость
	speedRegex := regexp.MustCompile(`at\s+(\d+\.?\d*\s*[KMGT]i?B/s)`)
	speedMatch := speedRegex.FindStringSubmatch(line)
	speed := ""
	if len(speedMatch) >= 2 {
		speed = speedMatch[1]
	}

	// Извлекаем ETA
	etaRegex := regexp.MustCompile(`ETA\s+(\d+:\d+)`)
	etaMatch := etaRegex.FindStringSubmatch(line)
	eta := ""
	if len(etaMatch) >= 2 {
		eta = etaMatch[1]
	}

	return int(percent), downloaded, total, speed, eta
}

// findDownloadedFile находит скачанный файл
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
func (d *Downloader) downloadViaSaveFrom(ctx context.Context, videoURL string, progressCallback ProgressCallback) (string, error) {
	d.logger.Info("Trying SaveFrom fallback", slog.String("url", videoURL))

	downloadURL, err := d.getSaveFromURL(ctx, videoURL)
	if err != nil {
		return "", fmt.Errorf("savefrom failed: %w", err)
	}

	return d.downloadFromURL(ctx, downloadURL, videoURL, progressCallback)
}

// getSaveFromURL получает прямую ссылку через SaveFrom
func (d *Downloader) getSaveFromURL(ctx context.Context, videoURL string) (string, error) {
	apiURL := fmt.Sprintf("https://savefrom.net/save-from.php?url=%s", url.QueryEscape(videoURL))

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
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

	bodyStr := string(body)

	// Ищем ссылку в JSON
	var jsonResponse struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &jsonResponse); err == nil && jsonResponse.URL != "" {
		return jsonResponse.URL, nil
	}

	// Ищем mp4 ссылки
	videoURLPattern := regexp.MustCompile(`(https?://[^"\s]+\.mp4[^"\s]*)`)
	matches := videoURLPattern.FindStringSubmatch(bodyStr)
	if len(matches) > 0 {
		return matches[1], nil
	}

	// Ищем googlevideo ссылки
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
func (d *Downloader) downloadFromURL(ctx context.Context, downloadURL, originalURL string, progressCallback ProgressCallback) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, http.NoBody)
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

	totalSize := resp.ContentLength
	outputFile := filepath.Join(d.tempDir, fmt.Sprintf("yt_savefrom_%d.mp4", time.Now().Unix()))

	file, err := os.Create(outputFile)
	if err != nil {
		return "", fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	// Если есть callback, отслеживаем прогресс
	var reader io.Reader = resp.Body
	if progressCallback != nil && totalSize > 0 {
		reader = &progressReader{
			reader:   resp.Body,
			total:    totalSize,
			callback: progressCallback,
		}
	}

	_, err = io.Copy(file, reader)
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

// progressReader оборачивает reader для отслеживания прогресса
type progressReader struct {
	reader      io.Reader
	total       int64
	downloaded  int64
	callback    ProgressCallback
	lastPercent int
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.downloaded += int64(n)

	if pr.total > 0 {
		percent := int(float64(pr.downloaded) * 100 / float64(pr.total))
		// Обновляем только при изменении процента (минимум на 5%)
		if percent >= pr.lastPercent+5 || percent == 100 {
			pr.callback(percent, pr.downloaded, pr.total, "", "")
			pr.lastPercent = percent
		}
	}

	return n, err
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

// GetVideoTitle возвращает название видео
func (d *Downloader) GetVideoTitle(ctx context.Context, videoURL string) (string, error) {
	args := []string{
		videoURL,
		"--get-title",
		"--no-playlist",
		"--no-warnings",
	}

	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get video title: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}
