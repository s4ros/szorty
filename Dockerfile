FROM golang:1.27.0 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /szort ./cmd/szort

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /szort /szort

USER 65532:65532

ENTRYPOINT ["/szort"]
