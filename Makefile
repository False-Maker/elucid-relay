.PHONY: build up down healthz portal admin migrate-up smoke-api

build:
	docker compose build

up:
	docker compose up --build

down:
	docker compose down

healthz:
	curl -fsS http://localhost:$${GATEWAY_API_PORT:-18080}/healthz

portal:
	curl -fsS http://localhost:$${PORTAL_PORT:-18081}/

admin:
	curl -fsS http://localhost:$${ADMIN_PORT:-18082}/

migrate-up:
	docker compose run --rm gateway-api migrate up

smoke-api:
	bash infra/smoke-api.sh
