# syntax=docker/dockerfile:1

# 1) Build the frontend
FROM node:22-alpine AS web
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# 2) Build the Go server (static binary)
FROM golang:1.24-alpine AS api
WORKDIR /app
COPY backend/go.mod ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /server ./cmd/api

# 3) Minimal runtime image serving API + WebSockets + the built frontend
FROM alpine:3.20
WORKDIR /app
COPY --from=api /server /app/server
COPY --from=web /app/dist /app/web
COPY seed/ /app/seed/
ENV PORT=8080
ENV STATIC_DIR=/app/web
EXPOSE 8080
USER nobody
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s CMD wget -q -O - "http://127.0.0.1:${PORT}/readyz" >/dev/null || exit 1
CMD ["/app/server"]
