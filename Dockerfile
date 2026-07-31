FROM golang:1.26@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647 AS builder
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) go build -o /out/transform ./

FROM gcr.io/distroless/static-debian12:nonroot
LABEL com.docker.compose.bridge=transformation
COPY --from=builder /out/transform /transform
ENTRYPOINT ["/transform"]
