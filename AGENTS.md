# Repository Guidelines

## Project Structure & Module Organization
- `cmd/wecom-robot/` – Program entry (`main.go`).
- `internal/config/` – Env configuration (`WECOM_TOKEN`, `WECOM_ENCODING_AES_KEY`, `WECOM_RECEIVE_ID`, `PORT`).
- `internal/server/` – HTTP routing and handlers (`/callback`).
- `internal/wecom/` – WeCom signature + AES-256-CBC/PKCS7 crypto utilities.
- Tests live alongside packages as `*_test.go`.

## Build, Test, and Development Commands
- Build: `go build ./...` – Compiles all packages.
- Run: `go run ./cmd/wecom-robot` – Starts the callback server.
  - In restricted environments use: `GOCACHE=$(pwd)/.gocache go run ./cmd/wecom-robot`.
- Format: `go fmt ./...` – Applies canonical Go formatting.
- Vet: `go vet ./...` – Static checks for common issues.
- Tidy: `go mod tidy` – Maintain clean module deps.
- Test: `go test ./... -cover` – Runs tests with coverage.

## Coding Style & Naming Conventions
- Use `go fmt` and idiomatic Go. One file per focused concern.
- Package names: short, lowercase (no underscores, no plurals where possible).
- Exported identifiers: `CamelCase` with concise doc comments.
- Error handling: wrap with context (`fmt.Errorf("...: %w", err)`).
- Logging: use `log.Printf`; avoid logging secrets or raw keys.

## Testing Guidelines
- Place tests in the same package, filename `*_test.go`.
- Prefer table-driven tests; cover edge cases (bad signatures, padding, wrong receiveid).
- Keep networkless, fast unit tests; use `go test -race` when practical.

## Commit & Pull Request Guidelines
- Commits: follow Conventional Commits (`feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`).
- PRs must include: purpose/summary, linked issue (if any), test notes, and manual run steps.
- Before opening a PR: `go fmt`, `go vet`, `go test ./...`, and ensure `go mod tidy` yields a clean diff.

## Security & Configuration Tips
- Never commit tokens/keys. Load from environment only.
- Validate `receiveid` (CorpID or SuiteID) when decrypting; required to encrypt replies.
- Keep response times < 5s to avoid WeCom retries.
- On failure to encrypt reply, return plaintext `success` to prevent retry storms.

