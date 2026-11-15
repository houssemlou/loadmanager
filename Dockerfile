# --- build ---
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/loadmanager ./cmd/loadmanager

# --- runtime: static binary, no shell, non-root ---
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/loadmanager /app/loadmanager
COPY configs /app/configs
ENV CONFIG_PATH=/app/configs/example-station.json \
    LISTEN_ADDR=:8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/loadmanager"]
