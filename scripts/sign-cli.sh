#!/bin/sh
set -eu

if [ "$#" -ne 1 ] || [ -z "${MAIL_APP_CLI_SIGN_IDENTITY:-}" ]; then
  echo 'Usage: MAIL_APP_CLI_SIGN_IDENTITY="Developer ID Application: ..." scripts/sign-cli.sh /absolute/path/mail-app-cli' >&2
  exit 1
fi
case "$1" in
  /*) ;;
  *) echo 'The binary path must be absolute.' >&2; exit 1 ;;
esac
if [ ! -f "$1" ] || [ ! -s "$1" ] || [ ! -x "$1" ] || [ -L "$1" ]; then
  echo 'The binary must be an existing executable regular file, not a symlink.' >&2
  exit 1
fi

# Keep this identifier and the signing team stable across upgrades so Keychain
# can recognize the installed CLI's designated requirement.
codesign --force --sign "$MAIL_APP_CLI_SIGN_IDENTITY" \
  --identifier ai.smw.mail-app-cli --timestamp=none "$1"
codesign --verify --strict "$1"
