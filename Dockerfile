FROM golang:1.27-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o /app/bin/reflex main.go

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/bin/reflex .
COPY runbooks.yaml .
COPY scripts ./scripts

EXPOSE 8080 9090

CMD ["./reflex"]
