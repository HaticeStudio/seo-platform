FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/seo-platform ./cmd/seo-platform

FROM node:22-alpine AS console
WORKDIR /console
COPY console/package.json console/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY console/ .
RUN npm run build

FROM alpine:3.20
RUN addgroup -S seo && adduser -S seo -G seo && apk add --no-cache ca-certificates \
    && mkdir -p /app/data && chown -R seo:seo /app
USER seo
WORKDIR /app
COPY --from=build /out/seo-platform /usr/local/bin/seo-platform
COPY --from=console /console/dist/app /app/console
ENV SEO_CONSOLE_DIR=/app/console
VOLUME ["/app/data"]
EXPOSE 8080
ENTRYPOINT ["seo-platform"]
