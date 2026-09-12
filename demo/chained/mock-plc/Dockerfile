# mock-plc: simulated Modbus TCP PLC
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -o /mock-plc .

FROM gcr.io/distroless/static-debian12
COPY --from=build /mock-plc /mock-plc
ENTRYPOINT ["/mock-plc"]
