FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /gitbeak-mcp ./cmd/mcp

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -u 1000 mcp

COPY --from=builder /gitbeak-mcp /usr/local/bin/gitbeak-mcp

USER mcp
ENTRYPOINT ["gitbeak-mcp"]
