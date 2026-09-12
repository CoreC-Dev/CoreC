# Multi-stage build for CoreC binary — shared by all chained-core scenarios.
# Build context must be the repository root:
#   docker build -t corec:demo -f demo/chained/corec.Dockerfile .
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.version=demo" \
    -o /corec ./cmd/corec

FROM gcr.io/distroless/static-debian12
COPY --from=build /corec /corec
ENTRYPOINT ["/corec"]
