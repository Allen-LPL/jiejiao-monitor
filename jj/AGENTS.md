# AGENTS.md

## Project Overview

Attendance check service ("jiejiao") written in Go. Periodically polls an attendance API and sends reminders via Feishu and DingTalk webhooks when a time slot is missed. Exposes an HTTP query endpoint for on-demand status checks.

Single-file codebase: all logic lives in `main.go` (~770 lines). No external dependencies — stdlib only.

## Build / Run / Test

```bash
# Build
go build -o jiejiao main.go

# Cross-compile for Linux
GOOS=linux GOARCH=amd64 go build -o jj-linux-amd64 main.go

# Run (requires config.json in working directory)
./jiejiao

# Run directly
go run main.go

# Vet (static analysis — no third-party linter configured)
go vet ./...

# Run tests (none exist yet — add *_test.go files alongside main.go)
go test ./...

# Run a single test
go test -run TestFunctionName ./...

# Run tests with verbose output
go test -v ./...

# Run tests with race detector
go test -race ./...
```

No Makefile, CI pipeline, or linter config exists. `go vet` is the only available static check.

## Project Structure

```
.
├── main.go            # All application code
├── go.mod             # Module: jiejiao, Go 1.24.2
├── config.json        # Runtime config (API URLs, webhooks, time slots)
├── API.md             # HTTP API docs for /api/check
├── jiejiao            # Compiled macOS binary
└── jj-linux-amd64     # Compiled Linux binary
```

## Tech Stack

- **Go 1.24.2** — stdlib only, zero external deps
- **net/http** — HTTP server and client
- **encoding/json** — config loading and API serialization
- **crypto/hmac + crypto/sha256** — webhook signature generation
- **compress/gzip** — response decompression

## Code Style

### Language & Comments

All comments and user-facing strings are in **Chinese (中文)**. Follow this convention for log messages, error messages, struct doc-comments, and notification content.

### Imports

Single grouped import block. Stdlib only — no third-party packages.

```go
import (
    "bytes"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)
```

### Naming

- **Structs**: PascalCase — `NotifyMessage`, `TimeSlotConfig`, `FeishuNotifier`
- **Fields**: PascalCase with json tags — `HTTPPort string \`json:"http_port"\``
- **Functions**: PascalCase for exported, camelCase for unexported — `NewFeishuNotifier`, `genFeishuSign`
- **Variables**: camelCase — `notifyManager`, `checkedCount`
- **Constants**: camelCase — `userAgent`
- **Interfaces**: PascalCase, behavior-named — `Notifier`

### Struct Definitions

Always include json struct tags. Use pointer types for optional/nullable fields.

```go
type AttendanceData struct {
    ID         int      `json:"id"`
    ReportTime *string  `json:"reportTime"`   // pointer = nullable
    Latitude   *float64 `json:"latitude"`
}
```

### Error Handling

Wrap errors with `fmt.Errorf` and `%w` verb. Chinese error descriptions. No panics in business logic — log and return.

```go
if err != nil {
    return nil, fmt.Errorf("请求失败: %w", err)
}
```

For non-fatal errors in goroutines, log and continue:

```go
go func(notifier Notifier) {
    if err := notifier.Send(msg); err != nil {
        log.Printf("通知器 [%s] 发送失败: %v", notifier.Name(), err)
    }
}(n)
```

### HTTP Patterns

- Use `http.HandleFunc` for routing (no router library)
- Set `Content-Type`, CORS headers in response helper
- 10-30 second client timeouts
- API key auth via query param (`?key=`) or header (`X-API-Key`)

```go
func writeJSONResponse(w http.ResponseWriter, statusCode int, data any) {
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    w.Header().Set("Access-Control-Allow-Origin", "*")
    w.WriteHeader(statusCode)
    json.NewEncoder(w).Encode(data)
}
```

### Concurrency

- `sync.Once` for one-time initialization (timezone loading)
- Goroutines for parallel notification dispatch
- `time.NewTicker` for periodic polling loop

### Architecture Pattern

Interface-based notifiers. Add new channels by:
1. Implementing the `Notifier` interface (`Name()` + `Send()`)
2. Registering in the `NewNotifyManager` switch

```go
type Notifier interface {
    Name() string
    Send(message NotifyMessage) error
}
```

### Section Separators

Code is organized with Chinese comment banners:

```go
// ==================== 配置结构定义 ====================
// ==================== 飞书通知器 ====================
// ==================== 业务逻辑 ====================
```

Preserve this convention when adding new sections.

## Configuration

`config.json` in working directory. Contains sensitive tokens — **never commit real credentials**.

Key fields: `attendance_api`, `auth_token`, `api_key`, `http_port`, `notifiers[]`, `time_slots[]`.

## API

Single endpoint: `GET /api/check?key=<api_key>` — returns JSON with slot-level attendance status. See `API.md` for full docs.

## Common Pitfalls

- Binary `jiejiao` and `jj-linux-amd64` are checked into the repo — rebuild after code changes
- `config.json` contains real auth tokens and webhook secrets — handle carefully
- The service hardcodes a mobile User-Agent string to mimic the attendance app
- Time checks run every 2 minutes (even minutes only) within configured slot windows
- All timestamps use `Asia/Shanghai` timezone
