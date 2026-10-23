#!/usr/bin/env bash
set -euo pipefail

# This entry point never loads .env. Supply only an isolated database's values
# explicitly; the workflow uses a fresh MySQL service, never the developer DB.
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
mode="${1:-all}"
case "$mode" in
  unit|integration|all) ;;
  *) printf 'Usage: bash scripts/test-ci-linux.sh [unit|integration|all]\n' >&2; exit 2 ;;
esac

export GOTOOLCHAIN=local
expected_go="$(awk '$1 == "go" {print $2; exit}' go.mod)"
actual_go="$(go env GOVERSION)"
if [[ -z "$expected_go" || "$actual_go" != "go$expected_go" ]]; then
  printf 'Go toolchain mismatch: go.mod=%s, installed=%s\n' "$expected_go" "$actual_go" >&2
  exit 1
fi
if [[ "$(go env GOOS)" != linux || "$(go env CGO_ENABLED)" != 1 ]]; then
  printf 'This entry point requires Linux and CGO_ENABLED=1 for race checks.\n' >&2
  exit 1
fi
cc="$(go env CC)"
if ! command -v "$cc" >/dev/null 2>&1; then
  printf 'Configured C compiler is unavailable: %s\n' "$cc" >&2
  exit 1
fi
go version
"$cc" --version
go mod download
go mod verify

if [[ "$mode" == unit || "$mode" == all ]]; then
  unformatted="$(gofmt -l cmd internal tests)"
  if [[ -n "$unformatted" ]]; then
    printf 'Unformatted Go files:\n%s\n' "$unformatted" >&2
    exit 1
  fi
  go vet ./...
  go test -count=1 ./...
  go test -race -count=1 ./...
fi

if [[ "$mode" == integration || "$mode" == all ]]; then
  # Enforce an explicitly named disposable base database for the CLI migration.
  # Individual tests create and remove their own random order_api_it_* schemas.
  : "${DB_HOST:?Set DB_HOST to an isolated MySQL service}"
  : "${DB_PORT:?Set DB_PORT to an isolated MySQL service port}"
  : "${DB_NAME:?Set DB_NAME to a disposable *_ci database}"
  : "${DB_USER:?Set DB_USER}"
  : "${DB_PASSWORD:?Set DB_PASSWORD}"
  : "${MYSQL_ROOT_PASSWORD:?Set MYSQL_ROOT_PASSWORD for isolated test schemas}"
  if [[ "$DB_NAME" != *_ci ]]; then
    printf 'Refusing CI migrations: DB_NAME must end with _ci.\n' >&2
    exit 1
  fi
  export TEST_MYSQL_HOST="${TEST_MYSQL_HOST:-$DB_HOST}"
  export TEST_MYSQL_PORT="${TEST_MYSQL_PORT:-$DB_PORT}"
  export CI_EXPECT_CLEAN_DATABASES=1
  # Run the read-only residue assertion even after an ordinary command failure
  # or test panic. Preserve the failure status; never drop leftover schemas here.
  verify_integration_cleanup() {
    local original_status=$?
    trap - EXIT
    if ! go test -tags=integration -run '^TestCINoResidualDatabases$' -count=1 ./tests/integration; then
      original_status=1
    fi
    exit "$original_status"
  }
  trap verify_integration_cleanup EXIT
  go run ./cmd/migrate up
  go run ./cmd/migrate status
  go vet -tags=integration ./...
  go test -tags=integration -count=1 -v ./tests/integration
  # A separate invocation includes all real database concurrency cases under
  # the race detector; it does not replace or disable the normal suite.
  go test -race -tags=integration -count=1 -v ./tests/integration
fi
