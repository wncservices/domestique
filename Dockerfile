# Both build stages run on the *build* platform and cross-compile, rather than
# being emulated under QEMU for each target. The frontend bundle is just files,
# and Go cross-compiles natively — so a two-architecture build costs barely more
# than a one-architecture build. That matters now that every merge to main
# builds an image.

FROM --platform=$BUILDPLATFORM node:24.19.0-alpine AS web
WORKDIR /src
COPY package.json package-lock.json ./
COPY apps/web/package.json apps/web/
RUN npm ci
COPY apps/web apps/web
RUN npm --workspace @domestique/web run build

FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine AS api
WORKDIR /src
COPY apps/api/go.mod apps/api/go.sum apps/api/
RUN cd apps/api && go mod download
COPY apps/api apps/api
ARG TARGETOS
ARG TARGETARCH
# Stamped so `domestique version` in a running container says which build it is.
# Dev builds get the short SHA, releases get the tag.
ARG VERSION=dev
RUN cd apps/api && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/domestique ./cmd/domestique

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 domestique
WORKDIR /app
COPY --from=api /out/domestique /usr/local/bin/domestique
COPY --from=web /src/apps/web/dist /app/web
# /app is root-owned by default, so the SQLite fallback below could not create
# its own directory as uid 10001.
RUN mkdir -p /app/data && chown -R 10001:10001 /app

USER domestique
EXPOSE 8080

# No route data is baked in, and no volume is declared: routes, sync state and
# linked head units are all rows in one database. Point DOMESTIQUE_SOURCE_DSN at
# a PostgreSQL server and the container is stateless. It falls back to a SQLite
# file under /app/data, which is only useful if you mount something there.
ENTRYPOINT ["domestique"]
CMD ["serve", "--addr", ":8080", "--config", "/app/domestique.yaml", \
     "--web-dir", "/app/web"]
