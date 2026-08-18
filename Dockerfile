FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/seo-platform ./cmd/seo-platform

FROM alpine:3.20
RUN addgroup -S seo && adduser -S seo -G seo && apk add --no-cache ca-certificates
USER seo
WORKDIR /app
COPY --from=build /out/seo-platform /usr/local/bin/seo-platform
VOLUME ["/app/data"]
EXPOSE 8080
ENTRYPOINT ["seo-platform"]
