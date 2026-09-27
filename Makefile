.PHONY: web-install web build test

web-install:
	npm --prefix web ci

web:
	npm --prefix web run build

build: web
	go build ./...

test:
	go test -race -count=1 ./...
