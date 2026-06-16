# syntax=docker/dockerfile:1.7

FROM golang:1.25-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -ldflags="-s -w" -o /out/redge ./cmd/redge

FROM alpine:3.22
WORKDIR /app

RUN addgroup -S redge && adduser -S -G redge redge \
	&& apk add --no-cache ca-certificates tzdata \
	&& mkdir -p /data \
	&& chown -R redge:redge /app /data

COPY --from=builder /out/redge /usr/local/bin/redge

ENV REDGE_ENV=production \
	DATABASE_URL=sqlite:///data/redge.db \
	REDGE_ADDR=0.0.0.0:6379 \
	REDGE_HTTP_ENABLED=true \
	REDGE_ADMIN_ENABLED=false

VOLUME ["/data"]
EXPOSE 6379 8080 9090

USER redge

CMD ["sh", "-c", "export REDGE_HTTP_ADDR=${REDGE_HTTP_ADDR:-0.0.0.0:${PORT:-8080}}; if [ -n \"${ADMIN_PORT:-}\" ]; then export REDGE_ADMIN_ADDR=0.0.0.0:${ADMIN_PORT}; fi; exec /usr/local/bin/redge"]
