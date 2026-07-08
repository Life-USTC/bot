FROM golang:1.25-bookworm AS build

WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/life-ustc-bot ./cmd/life-ustc-bot

FROM debian:bookworm-slim

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

RUN apt-get update \
	&& apt-get install -y --no-install-recommends fonts-noto-cjk \
	&& rm -rf /var/lib/apt/lists/* \
	&& mkdir -p /data \
	&& chown 10001:10001 /data

WORKDIR /app
COPY --from=build /out/life-ustc-bot /usr/local/bin/life-ustc-bot

ENV BOT_DB_PATH=/data/life-ustc-bot.db

USER 10001:10001
ENTRYPOINT ["life-ustc-bot"]
