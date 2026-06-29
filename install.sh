#!/bin/sh
set -eu

module="github.com/0xSMW/mail.app-cli"
version="${MAIL_APP_CLI_VERSION:-latest}"
binary="mail-app-cli"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "mail-app-cli requires macOS because it automates Mail.app." >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "Go 1.21 or newer is required: https://go.dev/dl/" >&2
  exit 1
fi

echo "Installing ${module}@${version}..."
go install "${module}@${version}"

gobin="$(go env GOBIN)"
if [ -z "$gobin" ]; then
  gobin="$(go env GOPATH)/bin"
fi

installed="${gobin}/${binary}"
if [ ! -x "$installed" ]; then
  echo "Install finished, but ${installed} was not found." >&2
  exit 1
fi

case ":$PATH:" in
  *":$gobin:"*) ;;
  *)
    echo "Installed ${binary} to ${installed}."
    echo "Add ${gobin} to PATH to run ${binary} from any shell."
    exit 0
    ;;
esac

"$installed" --version
echo "Installed ${binary} to ${installed}."
