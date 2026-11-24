# Multi-stage build for ky_server_base

# Stage 1: Build React PWA Frontend
FROM node:26.13.1-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS frontend-builder
WORKDIR /app/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Stage 2: Build Go Standalone Binary
FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS backend-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=frontend-builder /app/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o ky_server_base ./cmd/server

# Stage 3: Minimal Production Container
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=backend-builder /app/ky_server_base /app/ky_server_base
# /app/backups is the optional mount for sealed local capsules; KY_BACKUP_DIR is set by the
# operator (compose does), so an image run bare keeps no local copies.
RUN mkdir -p /app/data /app/backups

ENV KY_PORT=8080
ENV KY_HOST=0.0.0.0
ENV KY_DATA_DIR=/app/data

EXPOSE 8080
VOLUME ["/app/data", "/app/backups"]

ENTRYPOINT ["/app/ky_server_base"]
