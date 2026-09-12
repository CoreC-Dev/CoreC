# http-sink: HTTP endpoint that logs received data
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod ./
COPY main.go .
RUN CGO_ENABLED=0 go build -o /http-sink .

FROM gcr.io/distroless/static-debian12
COPY --from=build /http-sink /http-sink
ENTRYPOINT ["/http-sink"]
