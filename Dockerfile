# The Aruma server image: the dashboard is built with Node, embedded in a static Go binary, and run
# on Alpine with the PostgreSQL 18 client tools that `aruma backup` and `aruma restore` call.
# compose.yaml builds it; `docker build --build-arg VERSION=v0.4.0 -t aruma .` works on its own.

FROM node:24-slim AS dashboard
WORKDIR /src/dashboard
COPY dashboard/package.json dashboard/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY dashboard/ ./
RUN npm run build

FROM golang:1.27.3 AS server
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=dashboard /src/dashboard/out/ internal/admin/web/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/aruma ./cmd/aruma \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/arumactl ./cmd/arumactl

FROM alpine:3.23
RUN apk add --no-cache postgresql18-client ca-certificates tzdata \
 && addgroup -S -g 10001 aruma \
 && adduser -S -D -H -u 10001 -G aruma aruma \
 && mkdir -p /var/lib/aruma/storage \
 && chown -R aruma:aruma /var/lib/aruma
COPY --from=server /out/aruma /out/arumactl /usr/local/bin/
COPY docker/entrypoint.sh /usr/local/bin/aruma-entrypoint
USER aruma
WORKDIR /var/lib/aruma
EXPOSE 8080
# The console's status endpoint answers without a session once the server is up.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/console/v1/status || exit 1
ENTRYPOINT ["aruma-entrypoint"]
CMD ["serve"]
