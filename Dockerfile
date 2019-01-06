FROM golang:1 AS build

ENV GOPATH="/q/go"

COPY . $GOPATH/src/go.spiff.io/xq-api
RUN go build -v -o /usr/local/bin/xq-api go.spiff.io/xq-api
RUN go get go.spiff.io/retrap

FROM voidlinux/voidlinux:latest

COPY adm/etc /etc
RUN rm -rfv /var/db/xbps/https___* /var/db/xbps/http___*
RUN xbps-install -Syu
RUN xbps-install -yu
RUN xbps-install -y snooze runit

COPY adm/sbin /usr/local/sbin
COPY adm/service /var/service

RUN /usr/local/sbin/sync-xbps \
    x86_64 x86_64-musl \
    i686 i686-musl \
    armv6l armv6l-musl \
    armv7l armv7l-musl \
    aarch64 aarch64-musl || :

COPY --from=build /usr/local/bin/xq-api /usr/local/bin/xq-api
COPY --from=build /q/go/bin/retrap /usr/local/bin/retrap

EXPOSE 8197
STOPSIGNAL SIGHUP
ENTRYPOINT ["retrap", "INT:HUP", "--", "runsvdir", "/var/service"]
