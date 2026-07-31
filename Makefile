.PHONY: build run migrate backup test

build:
	go build -o bin/RTKron.exe ./cmd/rtkron

run:
	./bin/RTKron.exe

migrate:
	# using goose or your chosen migration tool
	goose -dir migrations sqlite3 $(DB_PATH) up

backup:
	@echo "Run scripts/backup.ps1 on Windows to backup D:"

test:
	go test ./...
