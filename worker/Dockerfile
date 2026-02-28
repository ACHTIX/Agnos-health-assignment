# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy module files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary — use TARGETARCH for multi-platform support
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-w -s" -o /app/worker .

# Runtime stage — distroless for minimal attack surface
FROM gcr.io/distroless/static:nonroot

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/worker .

# Default environment variables
ENV APP_ENV=production
ENV WORKER_INTERVAL=60s
ENV HEALTH_PORT=8081

EXPOSE 8081

USER nonroot:nonroot

ENTRYPOINT ["./worker"]
