default:
    @just --list

test:
    go test -race -count=1 ./...

lint:
    golangci-lint run

check: lint test

run:
    go run ./cmd/pageguard

build:
    CGO_ENABLED=0 go build -o bin/pageguard ./cmd/pageguard

fmt:
    gofmt -s -w .
    goimports -w .

tidy:
    go mod tidy

clean:
    rm -rf bin/

# Full local stack: checkout demo → Prometheus → Alertmanager → pageguard (needs ../shipmetrics and .env)
demo:
    docker compose up -d --build
    @echo "pageguard:    http://localhost:8080/api/v1/investigations  (Authorization: Bearer demo-token)"
    @echo "prometheus:   http://localhost:9090/alerts"
    @echo "alertmanager: http://localhost:9093"
    @echo "The bad deploy lands ~90s after start; the alert fires ~1m later."

demo-logs:
    docker compose logs -f pageguard

demo-down:
    docker compose down -v

helm-lint:
    helm lint deploy/helm/pageguard
