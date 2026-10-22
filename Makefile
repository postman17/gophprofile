GO ?= go
GO_VERSION := $(shell awk '/^go / {print "go" $$2}' go.mod)
GOLANGCI_LINT_VERSION ?= v2.5.0
GOLANGCI_LINT ?= GOTOOLCHAIN=$(GO_VERSION) $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
BIN_DIR ?= bin
SERVER_BIN := $(BIN_DIR)/gophprofile-server
WORKER_BIN := $(BIN_DIR)/worker

DOCKER_COMPOSE ?= docker compose
COMPOSE_FILES ?= -f docker-compose.yaml

COVER_FILE ?= coverage.out
COVER_HTML ?= coverage.html

ifneq (,$(wildcard .env))
include .env
export
endif

.DEFAULT_GOAL := help
.PHONY: help build build-server build-worker run-server run-worker tidy fmt fmt-check vet lint test test-race cover cover-html check docker-build up up-build down down-volumes ps logs logs-server logs-worker restart clean

help: ## Показать список команд
	@awk 'BEGIN {FS = ":.*##"; printf "Использование: make <команда>\n\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

build: build-server build-worker ## Собрать server и worker в ./bin

build-server: ## Собрать только server
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(SERVER_BIN) ./cmd/gophprofile

build-worker: ## Собрать только worker
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(WORKER_BIN) ./cmd/worker

run-server: ## Запустить API локально (нужны переменные окружения)
	$(GO) run ./cmd/gophprofile

run-worker: ## Запустить worker локально (нужны переменные окружения)
	$(GO) run ./cmd/worker

tidy: ## Привести go.mod/go.sum в порядок
	$(GO) mod tidy

fmt: ## Отформатировать код
	$(GO) fmt ./...

fmt-check: ## Проверить форматирование (падает, если код не отформатирован)
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "Не отформатировано:"; echo "$$out"; exit 1; fi

vet: ## Статический анализ
	$(GO) vet ./...

lint: ## Запустить golangci-lint
	$(GOLANGCI_LINT) run ./...

test: ## Прогнать unit-тесты
	$(GO) test ./...

test-race: ## Тесты с детектором гонок
	$(GO) test -race ./...

cover: ## Тесты с покрытием в консоли
	$(GO) test ./... -coverprofile=$(COVER_FILE)
	$(GO) tool cover -func=$(COVER_FILE) | tail -1

cover-html: cover ## Отчёт о покрытии в coverage.html
	$(GO) tool cover -html=$(COVER_FILE) -o $(COVER_HTML)
	@echo "Отчёт: $(COVER_HTML)"

check: fmt-check vet lint test ## Полная проверка перед коммитом

docker-build: ## Собрать docker-образы
	$(DOCKER_COMPOSE) $(COMPOSE_FILES) build

up: ## Поднять окружение (postgres, minio, kafka, server, worker)
	$(DOCKER_COMPOSE) $(COMPOSE_FILES) up -d

up-build: ## Поднять окружение с пересборкой образов
	$(DOCKER_COMPOSE) $(COMPOSE_FILES) up -d --build

down: ## Остановить окружение
	$(DOCKER_COMPOSE) $(COMPOSE_FILES) down

down-volumes: ## Остановить окружение и удалить данные (БД, S3)
	$(DOCKER_COMPOSE) $(COMPOSE_FILES) down -v

ps: ## Статус контейнеров
	$(DOCKER_COMPOSE) $(COMPOSE_FILES) ps

logs: ## Логи всех контейнеров (Ctrl+C для выхода)
	$(DOCKER_COMPOSE) $(COMPOSE_FILES) logs -f --tail=100

logs-server: ## Логи API
	$(DOCKER_COMPOSE) $(COMPOSE_FILES) logs -f --tail=100 server

logs-worker: ## Логи worker
	$(DOCKER_COMPOSE) $(COMPOSE_FILES) logs -f --tail=100 worker

restart: ## Перезапустить server и worker
	$(DOCKER_COMPOSE) $(COMPOSE_FILES) restart server worker

clean: ## Удалить артефакты сборки и отчёты
	rm -rf $(BIN_DIR) $(COVER_FILE) $(COVER_HTML)
