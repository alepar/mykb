FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /mykb ./cmd/mykb

FROM alpine:3.21
COPY --from=builder /mykb /mykb
COPY migrations /migrations
EXPOSE 9090
CMD ["/mykb"]
