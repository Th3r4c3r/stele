FROM golang:1.23-alpine AS build
WORKDIR /src

COPY go.mod ./
# go.sum will be added when first dependency lands. Until then, only go.mod is needed.
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/stele ./cmd/stele

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/stele /stele
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/stele"]
