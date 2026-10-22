# syntax=docker/dockerfile:1
FROM golang:1.26.8 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/
# Managed workspaces may supply their outbound CA as a build secret. The CA is
# used only for downloads and is never copied into the image or committed.
RUN --mount=type=secret,id=proxy_ca \
    if [ -f /run/secrets/proxy_ca ]; then export SSL_CERT_FILE=/run/secrets/proxy_ca; fi; \
    go mod download && \
    CGO_ENABLED=0 go build -trimpath -o /out/mcpguard ./cmd/mcpguard && \
    CGO_ENABLED=0 go build -trimpath -o /out/mcpguard-connect ./cmd/mcpguard-connect

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mcpguard /usr/local/bin/mcpguard
COPY --from=build /out/mcpguard-connect /usr/local/bin/mcpguard-connect
ENTRYPOINT ["/usr/local/bin/mcpguard"]
