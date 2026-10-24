# syntax=docker/dockerfile:1.7

FROM golang:1.27.3-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/share-secrets .

FROM scratch

COPY --from=build /out/share-secrets /share-secrets

USER 65532:65532
EXPOSE 8080
ENV ADDR=:8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
	CMD ["/share-secrets", "healthcheck"]

ENTRYPOINT ["/share-secrets"]
