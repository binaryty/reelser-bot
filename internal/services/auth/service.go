package auth

import (
	"sync"

	"github.com/reelser-bot/internal/config"
	"go.uber.org/zap"
)

// Service отвечает за авторизацию пользователей по токенам
type Service struct {
	logger  *zap.Logger
	enabled bool

	mu           sync.RWMutex
	validTokens  map[string]struct{}
	allowedUsers map[int64]struct{}
}

// NewService создает новый сервис авторизации
func NewService(logger *zap.Logger, cfg config.AuthConfig) *Service {
	tokens := make(map[string]struct{})
	for _, t := range cfg.Tokens {
		tokens[t] = struct{}{}
	}

	return &Service{
		logger:       logger,
		enabled:      cfg.Enabled,
		validTokens:  tokens,
		allowedUsers: make(map[int64]struct{}),
	}
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

	s.allowedUsers[userID] = struct{}{}

	s.logger.Info("User authorized successfully",
		zap.Int64("user_id", userID),
	)

	return true
}
