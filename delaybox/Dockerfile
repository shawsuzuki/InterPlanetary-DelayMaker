FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git libpcap-dev gcc musl-dev

WORKDIR /app
COPY go.mod ./
COPY main.go .
RUN go mod tidy && go mod download
RUN CGO_ENABLED=1 GOOS=linux go build -o /delaybox main.go

# Runtime image
FROM alpine:latest

RUN apk add --no-cache \
    iproute2 \
    iptables \
    docker-cli \
    bash \
    libpcap

COPY --from=builder /delaybox /usr/local/bin/delaybox
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
