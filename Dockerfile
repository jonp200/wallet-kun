FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /wallet-kun ./cmd/wallet-kun

FROM alpine:3.24
RUN apk add --no-cache ca-certificates && addgroup -S wallet && adduser -S wallet -G wallet \
    && mkdir -p /app/logs && chown -R wallet:wallet /app
WORKDIR /app
COPY --from=build /wallet-kun /usr/local/bin/wallet-kun
USER wallet
ENV APP_ENV=production
EXPOSE 5000
ENTRYPOINT ["wallet-kun"]
