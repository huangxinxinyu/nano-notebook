.PHONY: bootstrap migrate seed start stop health reset test-go test-web

bootstrap:
	scripts/bootstrap

migrate:
	scripts/migrate

seed:
	scripts/seed

start:
	scripts/start

stop:
	scripts/stop

health:
	scripts/health

reset:
	scripts/reset

test-go:
	scripts/test-go

test-web:
	scripts/test-web
