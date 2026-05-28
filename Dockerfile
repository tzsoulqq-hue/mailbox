FROM docker.m.daocloud.io/library/golang:1.26-alpine AS builder

WORKDIR /app
ENV GOPROXY=https://goproxy.cn,direct
ENV PATH=/root/go/bin:$PATH

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache git protobuf-dev \
    && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1

COPY services/mailbox-api/go.mod services/mailbox-api/go.sum* ./
RUN go mod download

COPY proto ./proto
RUN mkdir -p /generated/pb \
    && protoc -I proto --go_out=/generated/pb --go-grpc_out=/generated/pb \
      proto/email.proto \
      proto/mailbox_register.proto \
      proto/mailbox_service.proto

COPY services/mailbox-api ./
RUN rm -rf pb \
    && cp -R /generated/pb ./pb \
    && go build -o /out/mailbox .

FROM docker.m.daocloud.io/library/alpine:latest

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /out/mailbox /app/bin/mailbox

EXPOSE 50051 8082
CMD ["/app/bin/mailbox"]
