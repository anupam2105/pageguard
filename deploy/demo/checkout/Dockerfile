# syntax=docker/dockerfile:1
# Demo-only fake service. Build context is the repository root.
FROM golang:1.26.11-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/checkout ./deploy/demo/checkout

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/checkout /usr/local/bin/checkout
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/checkout"]
