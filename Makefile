.PHONY: build install

build:
	go build

install: build
	go install
