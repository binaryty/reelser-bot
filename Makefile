.PHONY: run build lint clean test deps help install-tools docker-build docker-run docker-stop compose-up compose-down compose-logs compose-rebuild restart rebuild logs dev update

# Переменные
BINARY_NAME=reelser-bot
MAIN_PATH=./cmd/bot
BUILD_DIR=./bin
DOCKER_IMAGE?=reelser-bot
DOCKER_TAG?=latest
DOCKER_CONTAINER?=reelser-bot
DOCKER_COMPOSE?=docker compose
COMPOSE_FILE?=docker-compose.yml

# Цвета для вывода
GREEN=\033[0;32m
YELLOW=\033[1;33m
NC=\033[0m # No Color

help: ## Показать справку по командам
	@echo "$(GREEN)Доступные команды:$(NC)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(YELLOW)%-15s$(NC) %s\n", $$1, $$2}'

deps: ## Установить зависимости
	@echo "$(GREEN)Installing dependencies...$(NC)"
	go mod download
	go mod tidy

build: ## Собрать приложение
	@echo "$(GREEN)Building application...$(NC)"
	@if not exist $(BUILD_DIR) mkdir $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_PATH)
	@echo "$(GREEN)Build complete: $(BUILD_DIR)/$(BINARY_NAME)$(NC)"

run: ## Запустить приложение
	@echo "$(GREEN)Running application...$(NC)"
	go run $(MAIN_PATH)

lint: ## Запустить линтер
	@echo "$(GREEN)Running linter...$(NC)"
	@if command -v golangci-lint > /dev/null; then \
		golangci-lint run; \
	else \
		echo "$(YELLOW)golangci-lint not found. Installing...$(NC)"; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
		golangci-lint run; \
	fi

test: ## Запустить тесты
	@echo "$(GREEN)Running tests...$(NC)"
	go test -v ./...

clean: ## Очистить артефакты сборки
	@echo "$(GREEN)Cleaning build artifacts...$(NC)"
	rm -rf $(BUILD_DIR)
	rm -rf ./tmp
	go clean

install-tools: ## Установить необходимые инструменты
	@echo "$(GREEN)Installing tools...$(NC)"
	@echo "$(YELLOW)Installing yt-dlp...$(NC)"
	@if command -v yt-dlp > /dev/null; then \
		echo "yt-dlp already installed"; \
	else \
		echo "Please install yt-dlp manually: https://github.com/yt-dlp/yt-dlp"; \
	fi

docker-build: ## Собрать Docker образ
	@echo "$(GREEN)Building Docker image $(DOCKER_IMAGE):$(DOCKER_TAG)...$(NC)"
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-run: ## Запустить контейнер локально
	@echo "$(GREEN)Running Docker container $(DOCKER_CONTAINER)...$(NC)"
	docker run --rm \
		--name $(DOCKER_CONTAINER) \
		--env-file ./.env \
		-v $(CURDIR)/tmp:/app/tmp \
		$(DOCKER_IMAGE):$(DOCKER_TAG)

docker-stop: ## Остановить запущенный контейнер
	@echo "$(YELLOW)Stopping Docker container $(DOCKER_CONTAINER) (if running)...$(NC)"
	-@docker stop $(DOCKER_CONTAINER) >/dev/null 2>&1 || true

compose-up: ## Запустить docker-compose
	@echo "$(GREEN)Starting services via docker compose...$(NC)"
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) up -d --build

compose-down: ## Остановить docker-compose
	@echo "$(YELLOW)Stopping services via docker compose...$(NC)"
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) down

compose-logs: ## Показать логи docker-compose
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) logs -f

compose-rebuild: ## Полная пересборка и перезапуск (очистка кэша)
	@echo "$(YELLOW)Stopping services...$(NC)"
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) down
	@echo "$(GREEN)Cleaning build cache...$(NC)"
	docker system prune -f
	@echo "$(GREEN)Building and starting services...$(NC)"
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) up -d --build --no-cache
	@echo "$(GREEN)Done! Watching logs...$(NC)"
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) logs -f

restart: ## Быстрый рестарт без очистки кэша
	@echo "$(YELLOW)Restarting services...$(NC)"
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) restart
	@echo "$(GREEN)Done!$(NC)"

rebuild: ## Пересборка с очисткой кэша и запуск
	@echo "$(YELLOW)Stopping and removing containers...$(NC)"
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) down --rmi local
	@echo "$(GREEN)Rebuilding from scratch...$(NC)"
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) build --no-cache
	@echo "$(GREEN)Starting services...$(NC)"
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) up -d
	@echo "$(GREEN)Done!$(NC)"

logs: ## Показать последние логи
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) logs --tail=50

dev: ## Режим разработки (пересборка + логи)
	@echo "$(GREEN)Starting development mode...$(NC)"
	$(MAKE) rebuild
	@echo "$(GREEN)Watching logs...$(NC)"
	$(MAKE) compose-logs

update: ## Обновление yt-dlp и пересборка
	@echo "$(GREEN)Updating yt-dlp...$(NC)"
	$(DOCKER_COMPOSE) -f $(COMPOSE_FILE) exec reelser-bot pip3 install -U yt-dlp
	@echo "$(GREEN)yt-dlp updated!$(NC)"

