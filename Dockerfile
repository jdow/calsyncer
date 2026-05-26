FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /calsyncer ./cmd

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /calsyncer /usr/local/bin/calsyncer

ENTRYPOINT ["calsyncer"]
