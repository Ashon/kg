# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT

# Build the kgenesis infrastructure provider.
FROM golang:1.27 AS build

WORKDIR /workspace

# Dependencies first so a source-only change reuses the module layer.
COPY go.mod go.sum ./
RUN go mod download

COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/

ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG GIT_COMMIT=""
ARG BUILD_DATE=""

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w \
      -X github.com/Ashon/kgenesis/internal/version.Version=${VERSION} \
      -X github.com/Ashon/kgenesis/internal/version.GitCommit=${GIT_COMMIT} \
      -X github.com/Ashon/kgenesis/internal/version.BuildDate=${BUILD_DATE}" \
    -o manager ./cmd/manager

# distroless: the manager only needs to open TCP connections and talk SSH, so
# there is nothing for a shell to do in the image.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
