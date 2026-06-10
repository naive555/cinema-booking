.PHONY: up down seed logs test-concurrency

up: ## start the full stack (builds backend image)
	docker compose up --build -d

down: ## stop and remove containers + volumes
	docker compose down -v

seed: ## run the idempotent seed against the running stack
	SEED_ON_START=true docker compose up -d --force-recreate backend

logs: ## tail all container logs
	docker compose logs -f

test-concurrency: ## prove no double-booking: 50 goroutines race for the same seat
	@echo "Stack must be running: make up (with DEV_AUTH=true SEED_ON_START=true)"
	cd backend && go test -v -count=1 -timeout 120s -run TestConcurrent ./test/...
