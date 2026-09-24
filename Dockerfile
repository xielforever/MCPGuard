# Build Frontend
FROM node:18-alpine AS frontend
WORKDIR /app/web
# COPY web/package*.json ./
# RUN npm install
# COPY web/ .
# RUN npm run build
# Uncomment above when web dir is actually populated

# Build Backend
FROM golang:1.22-alpine AS backend
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o mcpguard cmd/mcpguard/main.go

# Final Image
FROM alpine:latest
WORKDIR /app
COPY --from=backend /app/mcpguard .
# COPY --from=frontend /app/web/dist ./web/dist
COPY configs/mcpguard.yaml ./mcpguard.yaml

EXPOSE 8080
ENTRYPOINT ["./mcpguard"]
