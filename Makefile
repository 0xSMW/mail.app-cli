.PHONY: build install

build:
	go build -o mail-app-cli ./cmd/mail-app-cli

install:
	go install ./cmd/mail-app-cli
