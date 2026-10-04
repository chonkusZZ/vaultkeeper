# Build targets:  docker build --target manager -t vaultkeeper-manager .
#                 docker build --target agent   -t vaultkeeper-agent .
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vk-manager ./cmd/manager \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vk-agent ./cmd/agent

FROM gcr.io/distroless/static-debian12 AS manager
COPY --from=build /out/vk-manager /vk-manager
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/vk-manager", "--data-dir", "/data", "--listen", ":8080"]

# The agent image has CA certificates (it downloads restic on first run) and a
# shell for debugging. Mount cifs-utils/nfs-common here if you need share mounts.
FROM debian:12-slim AS agent
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/vk-agent /usr/local/bin/vk-agent
VOLUME /data
ENTRYPOINT ["vk-agent", "run", "--data-dir", "/data"]
