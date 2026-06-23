FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /neondrop ./cmd/neondrop

FROM alpine:3.22
RUN addgroup -S neondrop && adduser -S -G neondrop neondrop
USER neondrop
WORKDIR /app
COPY --from=build /neondrop /usr/local/bin/neondrop
EXPOSE 8080
ENTRYPOINT ["neondrop", "-addr", ":8080", "-data", "/tmp/neondrop"]
