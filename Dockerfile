FROM golang:1.17 as builder

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/ cmd/
#COPY api/ api/
COPY controllers/ controllers/
COPY internal/ internal/
COPY config/ config/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o ripfs cmd/ripfs/main.go

#FROM gcr.io/distroless/static:nonroot
FROM ipfs/go-ipfs:v0.12.0
WORKDIR /
COPY --from=builder /workspace/ripfs .
#USER 65532:65532

ENTRYPOINT ["/ripfs", "manager"]
