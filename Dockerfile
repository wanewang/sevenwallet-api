FROM golang:1.25.12 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /wallet-api ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /wallet-api /wallet-api
EXPOSE 8080
ENTRYPOINT ["/wallet-api"]
