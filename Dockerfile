# Two images from one build: the operator (default target) and the loopback
# bridge of local cells (target bridge). Separate images keep operator
# updates from restarting app pods that carry the bridge.
FROM golang:1.27.4-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api api
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags=-s -o /out/ ./cmd/operator ./cmd/bridge

FROM gcr.io/distroless/static-debian13:nonroot AS bridge
COPY --from=build /out/bridge /bridge
ENTRYPOINT ["/bridge"]

FROM gcr.io/distroless/static-debian13:nonroot AS operator
COPY --from=build /out/operator /operator
ENTRYPOINT ["/operator"]
