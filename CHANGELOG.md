# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **M0 Foundation** — Go 1.26 project scaffold, strict `golangci-lint v2` config, Distroless nonroot Dockerfile, `Justfile` task runner
- HTTP server exposing `/healthz`, `/readyz`, `/metrics` (Prometheus) with graceful shutdown
- Structured logging via `log/slog` (JSON by default, text for dev)
- Env-based typed configuration with strict validation
- 5-job GitHub Actions CI: lint, race-detector tests, build + smoke, `govulncheck`, `gitleaks`
- ADR-0001 documenting language and stack choice
