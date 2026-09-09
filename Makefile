.PHONY: help deps build test lint up down logs migrate seed load reset

help: ## Показать список целей
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

deps: ## Загрузить зависимости
	go mod tidy

build: ## Собрать бинарь
	go build -o bin/api ./cmd/api

test: ## Юнит-тесты
	go test ./... -race -count=1

lint: ## go vet
	go vet ./...

up: ## Поднять стенд: nginx + N инстансов + redis + postgres
	docker compose up -d --build

down: ## Остановить стенд и удалить тома
	docker compose down -v

logs: ## Логи приложения
	docker compose logs -f api1 api2 api3

migrate: ## Применить миграции (идемпотентно; при первом старте их уже прогнал compose)
	docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U vote -d vote < migrations/0001_init.up.sql

seed: ## Создать демо-опрос через админский API
	./scripts/seed.sh

load: ## Нагрузочный тест (см. cmd/loadgen)
	go run ./cmd/loadgen -target http://localhost:8080 -rps 5000 -duration 60s

reset: ## Сбросить Redis, поднять generation и перезапустить инстансы (architecture.md §5.7)
	# Порядок шагов существенен, а не косметичен.
	#
	# 1. Очистить счётчики.
	# 2. Перезапустить инстансы — они держат снапшоты предыдущего прогона.
	# 3. И только теперь поднять generation.
	#
	# Если поднять номер до рестарта, живой инстанс успеет записать устаревший
	# снапшот уже с новым номером, то есть израсходует бамп впустую: следующая
	# запись упрётся в GREATEST при равных номерах, и цифра застрянет навсегда.
	docker compose exec -T redis redis-cli FLUSHALL
	docker compose restart api1 api2 api3
	docker compose exec -T postgres psql -U vote -d vote -c "UPDATE polls SET generation = generation + 1;"
