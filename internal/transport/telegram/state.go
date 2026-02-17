package telegram

import (
	"fmt"
	"sync"
	"time"
)

// QualityState хранит информацию о состоянии выбора качества
type QualityState struct {
	VideoURL  string
	MessageID int
	ChatID    int64
	CreatedAt time.Time
}

// StateManager управляет состоянием выбора качества для YouTube видео
type StateManager struct {
	mu     sync.RWMutex
	states map[string]*QualityState // key: "chatID:messageID"
	ttl    time.Duration
}

// NewStateManager создает новый менеджер состояний
func NewStateManager() *StateManager {
	sm := &StateManager{
		states: make(map[string]*QualityState),
		ttl:    5 * time.Minute,
	}
	// Запускаем cleanup в фоне
	go sm.cleanupLoop()
	return sm
}

// Set сохраняет состояние
func (sm *StateManager) Set(chatID int64, messageID int, videoURL string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key := sm.makeKey(chatID, messageID)
	sm.states[key] = &QualityState{
		VideoURL:  videoURL,
		MessageID: messageID,
		ChatID:    chatID,
		CreatedAt: time.Now(),
	}
}

// Get получает состояние
func (sm *StateManager) Get(chatID int64, messageID int) (*QualityState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	key := sm.makeKey(chatID, messageID)
	state, exists := sm.states[key]
	if !exists {
		return nil, false
	}

	// Проверяем TTL
	if time.Since(state.CreatedAt) > sm.ttl {
		return nil, false
	}

	return state, true
}

// Delete удаляет состояние
func (sm *StateManager) Delete(chatID int64, messageID int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key := sm.makeKey(chatID, messageID)
	delete(sm.states, key)
}

// makeKey создает ключ для map
func (sm *StateManager) makeKey(chatID int64, messageID int) string {
	return fmt.Sprintf("%d:%d", chatID, messageID)
}

// cleanupLoop периодически очищает устаревшие состояния
func (sm *StateManager) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		sm.cleanup()
	}
}

// cleanup удаляет устаревшие состояния
func (sm *StateManager) cleanup() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	for key, state := range sm.states {
		if now.Sub(state.CreatedAt) > sm.ttl {
			delete(sm.states, key)
		}
	}
}
