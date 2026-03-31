FROM golang:1.25 AS builder
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) go build -o /out/transform ./

FROM gcr.io/distroless/static-debian12:nonroot
LABEL com.docker.compose.bridge=transformation
COPY --from=builder /out/transform /transform
ENTRYPOINT ["/transform"]
