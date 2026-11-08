# syntax=docker/dockerfile:1

# ---- 前端构建 ----
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- 后端构建（交叉编译，无 CGO）----
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH TARGETVARIANT
ARG VERSION=dev COMMIT=none
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist/app ./web/dist/app
RUN GOARM=${TARGETVARIANT#v} CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w -X github.com/kukushouhou/DKStore/internal/version.Version=${VERSION} -X github.com/kukushouhou/DKStore/internal/version.Commit=${COMMIT}" \
      -o /out/dkstore ./cmd/dkstore

# ---- 运行镜像 ----
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/dkstore /usr/local/bin/dkstore
ENV DKSTORE_DATA_DIR=/data DKSTORE_LISTEN=:8780
VOLUME ["/data"]
EXPOSE 8780
LABEL dkstore.self="true"
ENTRYPOINT ["/usr/local/bin/dkstore"]
