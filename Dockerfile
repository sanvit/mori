# syntax=docker/dockerfile:1
# Dependencies are obtained ONLY at image build time. Browser playback is self-hosted.
FROM python:3.13-alpine AS assets
WORKDIR /src
COPY package.json ./
COPY tools/vendor.py tools/vendor.py
COPY web/preview.js web/preview.js
RUN python tools/vendor.py

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
COPY --from=assets /src/web/vendor web/vendor
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/mori ./cmd/mori

FROM alpine:3
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 app && mkdir /cache && chown 10001:10001 /cache
COPY --from=build /out/mori /usr/local/bin/mori
USER app
ENV CACHE_DIR=/cache
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD ["/usr/local/bin/mori", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/mori"]
