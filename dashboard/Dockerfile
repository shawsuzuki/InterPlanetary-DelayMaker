FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /dashboard main.go

FROM alpine:latest
COPY --from=builder /dashboard /usr/local/bin/dashboard
ENTRYPOINT ["dashboard"]
