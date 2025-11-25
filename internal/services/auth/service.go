package auth

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/reelser-bot/internal/config"
	"go.uber.org/zap"
)

// Service отвечает за авторизацию пользователей по токенам
type Service struct {
	logger  *zap.Logger
	enabled bool

	mu               sync.RWMutex
	validTokens      map[string]struct{}
	allowedUsers     map[int64]struct{}
	allowedUsersFile string
}

// NewService создает новый сервис авторизации
func NewService(logger *zap.Logger, cfg config.AuthConfig) *Service {
	tokens := make(map[string]struct{})
	for _, t := range cfg.Tokens {
		tokens[t] = struct{}{}
	}

	svc := &Service{
		logger:           logger,
		enabled:          cfg.Enabled,
		validTokens:      tokens,
		allowedUsers:     make(map[int64]struct{}),
		allowedUsersFile: strings.TrimSpace(cfg.AllowedUsersFile),
	}

	svc.loadAllowedUsersFromFile()

	return svc
}

// IsEnabled возвращает, включена ли авторизация
func (s *Service) IsEnabled() bool {
	return s != nil && s.enabled
}

// IsAuthorized проверяет, авторизован ли пользователь
func (s *Service) IsAuthorized(userID int64) bool {
	if !s.IsEnabled() {
		return true
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	_, ok := s.allowedUsers[userID]
	return ok
}

// TryAuthorize пытается авторизовать пользователя по токену
// Возвращает true, если токен валиден и пользователь авторизован
func (s *Service) TryAuthorize(userID int64, token string) bool {
	if !s.IsEnabled() {
		return true
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.validTokens[token]; !ok {
		s.logger.Warn("Invalid auth token attempt",
			zap.Int64("user_id", userID),
		)
		return false
	}

	if _, exists := s.allowedUsers[userID]; exists {
		return true
	}

	s.allowedUsers[userID] = struct{}{}
	if err := s.appendAllowedUserToFile(userID); err != nil {
		s.logger.Warn("Failed to persist allowed user",
			zap.Int64("user_id", userID),
			zap.Error(err),
		)
	}

	s.logger.Info("User authorized successfully",
		zap.Int64("user_id", userID),
	)

	return true
}

func (s *Service) loadAllowedUsersFromFile() {
	if s.allowedUsersFile == "" {
		return
	}

	file, err := os.Open(s.allowedUsersFile)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		s.logger.Warn("Failed to open allowed users file",
			zap.String("file", s.allowedUsersFile),
			zap.Error(err),
		)
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		id, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			s.logger.Warn("Invalid user id in allowed users file",
				zap.String("line", line),
				zap.String("file", s.allowedUsersFile),
				zap.Error(err),
			)
			continue
		}

		s.allowedUsers[id] = struct{}{}
	}

	if err := scanner.Err(); err != nil {
		s.logger.Warn("Failed to read allowed users file",
			zap.String("file", s.allowedUsersFile),
			zap.Error(err),
		)
	}
}

func (s *Service) appendAllowedUserToFile(userID int64) error {
	if s.allowedUsersFile == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(s.allowedUsersFile), 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("failed to create directory for allowed users file: %w", err)
	}

	file, err := os.OpenFile(s.allowedUsersFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open allowed users file: %w", err)
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	if _, err := fmt.Fprintf(writer, "%d\n", userID); err != nil {
		return fmt.Errorf("failed to write allowed user id: %w", err)
	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush allowed users writer: %w", err)
	}

	return nil
}
