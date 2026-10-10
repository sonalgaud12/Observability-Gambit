FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gambit ./cmd/gambit

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends stockfish ca-certificates \
    && rm -rf /var/lib/apt/lists/*
ENV STOCKFISH_PATH=/usr/games/stockfish
WORKDIR /app
COPY --from=build /out/gambit /usr/local/bin/gambit
ENTRYPOINT ["gambit"]
CMD ["--help"]
