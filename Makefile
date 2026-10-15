.PHONY: build install

build:
	go build -o mail-app-cli ./cmd/mail-app-cli
	@if [ -n "$$MAIL_APP_CLI_SIGN_IDENTITY" ]; then sh scripts/sign-cli.sh "$(CURDIR)/mail-app-cli"; fi

install:
	go install ./cmd/mail-app-cli
	@if [ -n "$$MAIL_APP_CLI_SIGN_IDENTITY" ]; then \
		mail_cli_bindir="$$(go env GOBIN)"; \
		if [ -z "$$mail_cli_bindir" ]; then \
			mail_cli_gopath="$$(go env GOPATH)"; mail_cli_bindir="$${mail_cli_gopath%%:*}/bin"; \
		fi; \
		sh scripts/sign-cli.sh "$$mail_cli_bindir/mail-app-cli"; \
	fi
