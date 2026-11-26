# Two images from one build: the operator with the platform API, the web
# console and the ops agent (default target) and the loopback bridge of local
# cells (target bridge). Separate images keep operator updates from
# restarting app pods that carry the bridge.
FROM node:26.13.1-trixie-slim@sha256:abe7e5a82b61a8ef67b315b85497c9e65173387ccf2f4a029248184b8e020be5 AS console
WORKDIR /src/console
COPY console/package.json console/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY api/openapi.yaml /src/api/openapi.yaml
COPY console ./
RUN npm run build

FROM golang:1.27.4-trixie@sha256:430985542100b871e108ccc00e8dcc5eef08ca14ee5825e98ebf563d8ffec4ee AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api api
COPY catalog catalog
COPY cmd cmd
COPY deploy/deploy.go deploy/node.sh deploy/ui.sh deploy/versions.env deploy/
COPY internal internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags=-s -o /out/ ./cmd/operator ./cmd/bridge ./cmd/api ./cmd/agent ./cmd/names

FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5 AS bridge
COPY --from=build /out/bridge /bridge
ENTRYPOINT ["/bridge"]

FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5 AS operator
COPY --from=build /out/operator /operator
COPY --from=build /out/api /api
COPY --from=build /out/agent /agent
COPY --from=build /out/names /names
COPY --from=console /src/console/dist /console
ENTRYPOINT ["/operator"]
