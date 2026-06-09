.PHONY: up down seed logs

up: ## start the full stack (builds backend image)
	docker compose up --build -d

down: ## stop and remove containers + volumes
	docker compose down -v

seed: ## run the idempotent seed against the running stack
	SEED_ON_START=true docker compose up -d --force-recreate backend

logs: ## tail all container logs
	docker compose logs -f
