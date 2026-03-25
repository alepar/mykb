FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /mykb-api ./cmd/mykb-api

FROM alpine:3.21
COPY --from=builder /mykb-api /mykb-api
COPY migrations /migrations
EXPOSE 9091
CMD ["/mykb-api"]
