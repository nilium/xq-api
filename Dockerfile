FROM golang:1 AS build

ENV GOPATH="/q/go"

COPY . $GOPATH/src/go.spiff.io/xq-api
RUN go build -v -o /usr/local/bin/xq-api go.spiff.io/xq-api

FROM busybox:glibc

COPY --from=build /usr/local/bin/xq-api /usr/local/bin/xq-api
EXPOSE 8197
ENTRYPOINT ["/usr/local/bin/xq-api", "-logtostderr"]
