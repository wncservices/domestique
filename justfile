# compose.yaml already reads .env automatically (docker compose's own
# behaviour); this makes the native recipes below (`api`, `demo`, `cli`, ...)
# do the same, so one .env file configures either path the same way.
set dotenv-load := true

default:
    @just --list

# --- in Docker (the only thing you need installed is Docker) ---
#
# Everything below this heading runs in a container, against the same
# PostgreSQL a deployment uses. The native recipes further down are the same
# work with a local Go and Node, which is quicker if you have them.

# Start PostgreSQL and the app on http://localhost:8080. Builds on first run.
up *ARGS:
    docker compose up --build --wait {{ARGS}}
    @echo "→ http://localhost:8080"

# Stop everything. The database survives; use `just reset` to drop it.
down:
    docker compose down

# Stop everything and throw the database away.
reset:
    docker compose down --volumes

logs *ARGS:
    docker compose logs --follow {{ARGS}}

# Install the Garmin app keys, so the sign-in form appears. Needs `just up`
# first; run it again after `just reset`.
garmin-keys:
    docker compose run --rm node node docker/garmin-keys.mjs

# The CLI inside the running app, same database. e.g. `just cli state`
cli *ARGS:
    docker compose exec app domestique {{ARGS}}

# The Go suite, against a real PostgreSQL. Same command CI runs.
docker-test *ARGS:
    docker compose run --rm go go test ./apps/api/... {{ARGS}}

# Typecheck, vet and test — everything CI checks, in containers.
docker-check:
    docker compose run --rm go sh -c 'gofmt -l apps/api | tee /dev/stderr | (! read)'
    docker compose run --rm go go vet ./apps/api/...
    docker compose run --rm go go test ./apps/api/...
    docker compose run --rm node sh -c 'npm ci && npm run typecheck'

# Frontend bundle + a binary in ./bin. Built for Linux, so a Mac cannot run it.
docker-build:
    docker compose run --rm node sh -c 'npm ci && npm run build'
    docker compose run --rm go go build -o bin/domestique ./apps/api/cmd/domestique

# --- setup ---

install:
    npm install
    cp -n domestique.example.yaml domestique.yaml || true

# --- dev ---

# Run the API (serves the built frontend if apps/web/dist exists).
api *ARGS:
    go run ./apps/api/cmd/domestique serve {{ARGS}}

# A local SQLite library with the example route loaded, for a look around.
demo:
    go run ./apps/api/cmd/domestique import --db ./data/demo.db --from examples/routes
    go run ./apps/api/cmd/domestique serve --db ./data/demo.db

# Run the Vue dev server with hot reload, proxying /api to the API on :8080.
web:
    npm --workspace @domestique/web run dev

# --- CLI ---

validate *ARGS:
    go run ./apps/api/cmd/domestique validate {{ARGS}}

plan *ARGS:
    go run ./apps/api/cmd/domestique plan {{ARGS}}

push *ARGS:
    go run ./apps/api/cmd/domestique push {{ARGS}}

# List or import routes from Komoot. Needs KOMOOT_EMAIL and KOMOOT_PASSWORD.
komoot *ARGS:
    go run ./apps/api/cmd/domestique komoot {{ARGS}}

# Write a route out as a FIT course, to copy onto a device over USB.
fit SLUG *ARGS:
    go run ./apps/api/cmd/domestique fit {{SLUG}} {{ARGS}}

# Load a folder of .gpx files into the database. A one-off, not a storage mode.
import FROM:
    go run ./apps/api/cmd/domestique import --db ./data/domestique.db --from {{FROM}}

# --- quality ---

fmt:
    gofmt -w apps/api
    go vet ./apps/api/...

lint: fmt
    npm --workspace @domestique/web run typecheck

test:
    go test ./apps/api/...

check: lint test

# --- build ---

build: build-web build-api

build-web:
    npm --workspace @domestique/web run build

build-api:
    go build -o bin/domestique ./apps/api/cmd/domestique

docker:
    docker build -t domestique:dev .
