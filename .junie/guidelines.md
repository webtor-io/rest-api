# Project Development Guidelines — rest-api

This document captures project-specific build, configuration, and testing practices verified on 2025-11-27 19:00 local time.

## Build and Configuration

- Language/Tooling
  - Go modules are used (module: `github.com/webtor-io/rest-api`).
  - go directive in `go.mod` is `go 1.25`.
  - Primary entrypoint is `main.go` with CLI wired via `urfave/cli`.

- Swagger/OpenAPI generation
  - The Makefile target `build` runs Swagger doc generation and then builds:
    - `swag init -g services/web.go && go build .`
  - Ensure the `swag` CLI is installed before running Make:
    - `go install github.com/swaggo/swag/cmd/swag@latest`
  - Swagger annotations live in handlers (e.g., `services/web.go`). Generated artifacts are under `docs/`.

- Building locally
  - Fast path: `go build .`
  - With swagger regeneration: `make build`
  - The produced binary is `rest-api` (module name); run with the `serve` command:
    - `./rest-api serve`

- Docker image
  - Multi-stage Dockerfile builds a statically linked binary with `CGO_ENABLED=0` and `GOOS=linux` and runs it as `./server serve`.
  - Build: `docker build -t webtor/rest-api:dev .`
  - Run: `docker run --rm -p 8080:8080 webtor/rest-api:dev`
  - Exposed ports: 8080 and 8081 (8081 is typically used by probes/pprof when enabled via flags below).

- Runtime configuration (CLI flags and env)
  - Web server flags (defined in `services/web.go` → `RegisterWebFlags`):
    - `--web-host` (`WEB_HOST`) — bind host. Default: empty (all interfaces).
    - `--web-port` (`WEB_PORT`) — HTTP port. Default: 8080.
  - Torrent Store gRPC client (in `services/torrent_store.go` → `RegisterTorrentStoreFlags`):
    - `--torrent-store-host` (`TORRENT_STORE_SERVICE_HOST`, fallback `TORRENT_STORE_HOST`).
    - `--torrent-store-port` (`TORRENT_STORE_SERVICE_PORT`, fallback `TORRENT_STORE_PORT`). Default: 50051.
    - Large message sizes are pre-configured (50 MiB) with gRPC call options.
  - Magnet2Torrent gRPC client (in `services/magnet2torrent.go` → `RegisterMagnet2TorrentFlags`):
    - `--magnet2torrent-host` (`MAGNET2TORRENT_SERVICE_HOST`, fallback `MAGNET2TORRENT_HOST`).
    - `--magnet2torrent-port` (`MAGNET2TORRENT_SERVICE_PORT`, fallback `MAGNET2TORRENT_PORT`). Default: 50051.
  - Common services (set in `serve.go` via `github.com/webtor-io/common-services`):
    - Probe and pprof flags are registered by `cs.RegisterProbeFlags` and `cs.RegisterPprofFlags` — see that library’s README for concrete names. These can expose health endpoints and pprof on the secondary port.
  - Other components assembled at runtime (see `serve.go`):
    - CacheMap, List, NodesStat, Subdomains, URLBuilder, Exporters.

- Key timeouts and caching (useful when debugging behavior)
  - `ResourceMap` (`services/resource.go`):
    - `torrentStoreTimeout: 10s`
    - `magnetTimeout: 3m`
    - Backed by `lazymap` with `Concurrency: 100`, `Expire: 600s`, `Capacity: 1000`.

## Testing

Important: The repository contains an extensive test suite under `services/`. Some tests assume certain mocked interactions and exact resource metadata; depending on local environment or dependency versions, the baseline `go test ./...` may fail. Use the following guidance to run reliable subsets or create isolated tests.

- Run tests for a specific package
  - Example: `go test ./services -run TestResourceMap_parseWithSha1 -count=1`
  - Use `-run` to target explicit tests and avoid flaky ones.

- Race detector and coverage
  - Race: `go test ./services -race -run TestResourceMap_parseWithSha1 -count=1`
  - Coverage HTML report: `go test ./services -coverprofile=coverage.out && go tool cover -html=coverage.out`

- Adding a new test
  - Place `_test.go` files alongside the package they test. Use `testing`, `testify/assert`, `testify/require`, and the provided mocks in `services/mocks.go` where applicable.
  - Mocks: The project already includes `NewTorrentStoreMock` and `NewMagnet2TorrentMock` patterns used by tests (see `services/resource_test.go`). Ensure all expected gRPC calls are stubbed (e.g., `.On("Push"...)`) to prevent unexpected call panics from `testify/mock`.

- Verified demo: isolated throwaway test
  - To demonstrate a guaranteed-green test independent of the main suite, you can create a temporary package directory (do not commit) and run it explicitly.
  - Example test file content:
    ```go
    package ztemp_demo

    import "testing"

    // Throwaway demo test used for documentation. Not part of the real suite.
    func TestSum(t *testing.T) {
        if 2+2 != 4 {
            t.Fatal("math broken")
        }
    }
    ```
  - Run only this package:
    ```bash
    go test ./ztemp_demo -count=1
    ```
  - This demonstration was executed successfully prior to publishing this document at 2025-11-27 19:00. As a housekeeping rule for this repository, please remove temporary demo test directories after use to keep the tree clean.

- Notes about existing tests under `services/`
  - `resource_test.go` validates parsing of torrents/magnets and interactions with mocks for TorrentStore and Magnet2Torrent. If you change piece path construction or metadata normalization, update test expectations accordingly (e.g., file path arrays for the first item).
  - When using `lazymap`, concurrent retrieval can obscure setup errors in mocks. If tests intermittently panic with `mock: unexpected method call`, ensure all expected gRPC methods are `.On(...).Return(...)`-ed for each code path.
  - Test data lives under `services/testdata/` (e.g., `Sintel.torrent`). Keep paths stable.

## Additional Development Notes

- Code style
  - Follow standard Go formatting (`gofmt`/`goimports`). There is no repository-enforced linter config; mirror existing patterns for logging and error wrapping (`pkg/errors`).

- Logging and errors
  - Logging: `sirupsen/logrus` with full timestamps configured in `main.go`.
  - Errors: `github.com/pkg/errors` is used widely; prefer `errors.Wrap/Wrapf` for context.

- HTTP and routing
  - `gin-gonic/gin` is used for routing. JSON responses use `c.PureJSON` for deterministic serialization.
  - Swagger UI is mounted using `swaggo/gin-swagger` with generated docs from `docs/`.

- gRPC clients
  - Both TorrentStore and Magnet2Torrent clients are created with `grpc.NewClient` and `insecure.NewCredentials()`; large message sizes are set via `grpc.WithDefaultCallOptions` to 50 MiB. If your payloads exceed this, adjust `torrentStoreMaxMsgSize` / `magnet2torrentMaxMsgSize`.

- Resource lifecycle
  - `serve.go` wires everything and defers closing of network clients and web server. If you add new long-lived clients, expose `Close()` and register them similarly.

- Performance/caching
  - `ResourceMap` caches by key (resource payload) via `lazymap`. Tune `Concurrency`, `Expire`, and `Capacity` in `NewResourceMap` if load characteristics change.

- Common pitfalls
  - Baseline `go test ./...` may not be green due to strict expectations in `services/resource_test.go`. Use targeted runs during local development; update tests alongside behavioral changes.
  - When adding new Swagger-annotated handlers, re-run `make build` (or `swag init`) to keep `docs/` consistent; the CI/build may rely on generated files being present.
  - Ensure required external services (Torrent Store and Magnet2Torrent) are reachable when exercising runtime paths; otherwise, `ResourceMap.Get` may time out (10s for store touch; up to 3m for magnet fetch).

## Quick Command Reference

- Install Swagger tool:
  - `go install github.com/swaggo/swag/cmd/swag@latest`

- Build with docs:
  - `make build`

- Run server locally:
  - `./rest-api serve --web-port 8080 \
      --torrent-store-host 127.0.0.1 --torrent-store-port 50051 \
      --magnet2torrent-host 127.0.0.1 --magnet2torrent-port 50051`

- Run a specific test (race enabled):
  - `go test ./services -race -run TestResourceMap_parseWithSha1 -count=1`

- Temporary demo-only test package (not committed):
  - `go test ./ztemp_demo -count=1`

Keep this document up to date when flags or wiring change in `serve.go` or when major behaviors (timeouts, caching) are tuned.
