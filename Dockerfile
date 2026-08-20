FROM --platform=$BUILDPLATFORM golang:1.27.0-alpine AS builder

ARG TARGETARCH

# proto → Go 绑定生成工具链（见 Taskfile.yml）
RUN apk add --no-cache protobuf \
    && go install github.com/go-task/task/v3/cmd/task@latest \
    && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11 \
    && go install connectrpc.com/connect/cmd/protoc-gen-connect-go@v1.19.1

WORKDIR /src

# 先 COPY 已提交的描述符并生成绑定（replace 目标 outputs/devin-proto-go 存在后，
# 后续 go mod download 才能解析 local/devinproto）
COPY Taskfile.yml ./
COPY outputs/devin-proto ./outputs/devin-proto
RUN task generate

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY e2e ./e2e

RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/devin-2api ./cmd/devin-2api

FROM alpine:3.22

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /out/devin-2api /app/devin-2api

EXPOSE 8080

ENTRYPOINT ["/app/devin-2api"]