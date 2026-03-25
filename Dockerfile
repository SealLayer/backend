# Build
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/server ./cmd/server

# Runtime: git + gnupg for clone/commit/push and detached signatures
FROM alpine:3.21
RUN apk add --no-cache ca-certificates git gnupg

WORKDIR /app
COPY --from=build /out/server /app/server

# Isolated keyring inside the container (mount a volume here in production if you need persistence across restarts)
RUN mkdir -p /app/.gnupg && chmod 700 /app/.gnupg

EXPOSE 8080

ENV HTTP_LISTEN_ADDR=":8080"
ENV GNUPGHOME="/app/.gnupg"

# Required at runtime (set via Coolify / compose / k8s — not baked into the image):
#   GITHUB_TOKEN, GITHUB_OWNER, GITHUB_REPO
#   GIT_AUTHOR_NAME, GIT_AUTHOR_EMAIL
#   GPG_KEYID, GPG_PASSPHRASE, GPG_PUBLIC_KEY_FINGERPRINT
# Optional: GPG_PRIVATE_KEY_ARMORED or GPG_PRIVATE_KEY_FILE (import before first use)
# Optional: GPG_PROGRAM, GIT_SIGN_COMMITS, BATCH_INTERVAL, ...

CMD ["/app/server"]
