ARG FRP_VERSION=v0.61.1
FROM docker.io/fatedier/frpc:${FRP_VERSION} AS frpc

FROM docker.m.daocloud.io/library/golang:1.26-alpine AS outlook_builder

WORKDIR /app
ENV GOPROXY=https://goproxy.cn,direct
ENV PATH=/root/go/bin:$PATH

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache git protobuf-dev \
    && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11 \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1

COPY providers/outlook/imap-service/go.mod providers/outlook/imap-service/go.sum* ./
RUN go mod download

COPY proto ./proto
COPY providers/outlook/imap-service ./
RUN mkdir -p pb \
    && rm -f pb/*.pb.go pb/*_grpc.pb.go \
    && protoc -I proto --go_out=pb --go-grpc_out=pb proto/email.proto \
    && go build -o /out/outlook-mailbox .

FROM docker.m.daocloud.io/library/golang:1.26-alpine AS mailbox_builder

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
COPY services/mailbox-api ./
RUN mkdir -p pb \
    && rm -f pb/*.pb.go pb/*_grpc.pb.go \
    && protoc -I proto --go_out=pb --go-grpc_out=pb \
      proto/email.proto \
      proto/mailbox_register.proto \
      proto/mailbox_service.proto \
    && go build -o /out/mailbox .

FROM docker.m.daocloud.io/library/alpine:latest

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache bash ca-certificates

WORKDIR /app
COPY --from=outlook_builder /out/outlook-mailbox /app/bin/outlook-mailbox
COPY --from=mailbox_builder /out/mailbox /app/bin/mailbox
COPY --from=frpc /usr/bin/frpc /usr/local/bin/frpc
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

EXPOSE 50051
CMD ["/app/entrypoint.sh"]
