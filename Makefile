.PHONY: up down build test frontend-test backend-test cli

up:
	docker compose up -d --build

down:
	docker compose down

build:
	docker compose build

test: backend-test frontend-test

backend-test:
	cd backend && go test ./...

frontend-test:
	cd frontend && npm test

cli:
	mkdir -p bin
	cd backend && go build -o ../bin/aihubctl ./cmd/aihubctl
