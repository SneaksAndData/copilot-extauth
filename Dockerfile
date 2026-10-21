FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /copilot-extauth .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /copilot-extauth /copilot-extauth
USER nonroot
EXPOSE 9001
ENTRYPOINT ["/copilot-extauth"]
