FROM golang:1 AS build

ENV GOPATH="/q/go"

COPY . $GOPATH/src/go.spiff.io/xq-api
RUN go build -v -o /usr/local/bin/xq-api go.spiff.io/xq-api

FROM voidlinux/voidlinux:latest

COPY adm/sbin /usr/local/sbin
COPY adm/etc /etc
COPY adm/service /var/service

RUN rm -r /var/db/xbps/http___repo_voidlinux_eu_current

RUN xbps-install -Syu
RUN xbps-install -yu
RUN xbps-install -y snooze

RUN /usr/local/sbin/sync-xbps \
    x86_64 x86_64-musl \
    i686 i686-musl \
    armv6l armv6l-musl \
    armv7l armv7l-musl \
    aarch64 aarch64-musl || :

COPY --from=build /usr/local/bin/xq-api /usr/local/bin/xq-api
EXPOSE 8197
STOPSIGNAL SIGHUP
ENTRYPOINT ["runsvdir", "/var/service"]
