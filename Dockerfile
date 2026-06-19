FROM docker.io/library/golang:1.25-bookworm AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags='-s -w' -o git-signing-proxy .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /app/git-signing-proxy /git-signing-proxy
ENTRYPOINT ["/git-signing-proxy"]
