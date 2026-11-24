# Two images from one build: the operator with the platform API, the web
# console and the ops agent (default target) and the loopback bridge of local
# cells (target bridge). Separate images keep operator updates from
# restarting app pods that carry the bridge.
FROM node:26.13.1-trixie-slim AS console
WORKDIR /src/console
COPY console/package.json console/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY api/openapi.yaml /src/api/openapi.yaml
COPY console ./
RUN npm run build

FROM golang:1.27.4-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api api
COPY catalog catalog
COPY cmd cmd
COPY deploy/deploy.go deploy/node.sh deploy/versions.env deploy/
COPY internal internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags=-s -o /out/ ./cmd/operator ./cmd/bridge ./cmd/api ./cmd/agent

FROM gcr.io/distroless/static-debian13:nonroot AS bridge
COPY --from=build /out/bridge /bridge
ENTRYPOINT ["/bridge"]

FROM gcr.io/distroless/static-debian13:nonroot AS operator
COPY --from=build /out/operator /operator
COPY --from=build /out/api /api
COPY --from=build /out/agent /agent
COPY --from=console /src/console/dist /console
ENTRYPOINT ["/operator"]
