FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.24-alpine AS app
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/flashshare .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S flashshare \
    && adduser -S -D -H -G flashshare flashshare \
    && mkdir -p /data \
    && chown flashshare:flashshare /data
USER flashshare
WORKDIR /data
COPY --from=app /out/flashshare /usr/local/bin/flashshare
ENV FLASHSHARE_DATA_DIR=/data \
    FLASHSHARE_LISTEN_ADDR=:8080
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["flashshare"]

