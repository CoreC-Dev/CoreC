# lora-sim: simulated LoRa gateway publishing 3rd-party JSON to MQTT
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -o /lora-sim .

FROM gcr.io/distroless/static-debian12
COPY --from=build /lora-sim /lora-sim
ENTRYPOINT ["/lora-sim"]
