version := `node -p "require('./web/package.json').version"`
release_dir := "release/action-control-" + version + "-linux-arm64"

# Show available recipes.
default:
    @just --list

# Install frontend dependencies.
install:
    npm --prefix web ci

# Build frontend assets for the embedded Go server.
assets:
    npm --prefix web run build
    rm -rf cmd/action-control/web/dist
    mkdir -p cmd/action-control/web
    cp -R web/dist cmd/action-control/web/dist

# Run the backend in local mode.
backend: assets
    mkdir -p /tmp/action-control-dev
    go run ./cmd/action-control serve --root /tmp/action-control-dev --listen 127.0.0.1:8080

# Run the frontend development server.
frontend:
    npm --prefix web run dev

# Run both local development servers.
dev:
    just --parallel backend frontend

# Run backend tests and verify the frontend build.
check: assets
    go test ./...

# Build a complete installable release archive.
release:
    node tools/build-ffmpeg.mjs
    rm -rf {{release_dir}} {{release_dir}}.tar.gz {{release_dir}}.tar.gz.sha256
    node tools/build-release.mjs

# Build and install an update on the connected camera.
update serial="": release
    ANDROID_SERIAL={{quote(serial)}} ./{{release_dir}}/install.sh --operation update
