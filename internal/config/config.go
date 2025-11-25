package config

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config содержит всю конфигурацию приложения
type Config struct {
	Telegram TelegramConfig
	Download DownloadConfig
	Log      LogConfig
	Auth     AuthConfig
}

// TelegramConfig содержит настройки Telegram-бота
type TelegramConfig struct {
	BotToken string
}

// DownloadConfig содержит настройки загрузки видео
type DownloadConfig struct {
	TempDir        string
	MaxVideoSizeMB int
	VideoQuality   string
	WorkerPoolSize int
}

// LogConfig содержит настройки логирования
type LogConfig struct {
	Level string
}

// AuthConfig содержит настройки авторизации пользователей
type AuthConfig struct {
	Enabled          bool
	Tokens           []string
	AllowedUsersFile string
}

// Load загружает конфигурацию из переменных окружения
func Load() (*Config, error) {
	// Загружаем .env файл, если он существует (игнорируем ошибку, если файла нет)
	_ = godotenv.Load()

	cfg := &Config{
		Telegram: TelegramConfig{
			BotToken: getEnv("TELEGRAM_BOT_TOKEN", ""),
		},
		Download: DownloadConfig{
			TempDir:        getEnv("TEMP_DIR", "./tmp"),
			MaxVideoSizeMB: getEnvAsInt("MAX_VIDEO_SIZE_MB", 50),
			VideoQuality:   getEnv("VIDEO_QUALITY", "best"),
			WorkerPoolSize: getEnvAsInt("WORKER_POOL_SIZE", runtime.NumCPU()),
		},
		Log: LogConfig{
			Level: getEnv("LOG_LEVEL", "info"),
		},
		Auth: AuthConfig{
			Enabled:          getEnvAsBool("AUTH_ENABLED", false),
			Tokens:           splitAndTrim(getEnv("AUTH_TOKENS", "")),
			AllowedUsersFile: getEnv("AUTH_ALLOWED_USERS_FILE", "./allowed_users.txt"),
		},
	}

	// Валидация обязательных полей
	if cfg.Telegram.BotToken == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is required")
	}

	return cfg, nil
}

// getEnv получает значение переменной окружения или возвращает значение по умолчанию
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvAsInt получает значение переменной окружения как int или возвращает значение по умолчанию
func getEnvAsInt(key string, defaultValue int) int {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}

	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return defaultValue
	}

	return value
}

// getEnvAsBool получает значение переменной окружения как bool или возвращает значение по умолчанию
func getEnvAsBool(key string, defaultValue bool) bool {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}

	switch strings.ToLower(valueStr) {
	case "1", "true", "t", "yes", "y":
		return true
	case "0", "false", "f", "no", "n":
		return false
	default:
		return defaultValue
	}
}

// splitAndTrim разбивает строку по запятой и обрезает пробелы
func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}

	parts := strings.Split(s, ",")
	var res []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			res = append(res, p)
		}
	}
	return res
}
