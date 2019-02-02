# GO
PACKAGE := go.spiff.io/xq-api
GO_SRC := $(shell go list -f '{{.ImportPath}}{{"\n"}}{{range .Deps}}{{.}}{{"\n"}}{{end}}' $(PACKAGE) | xargs go list -f '{{$$dir := .Dir}}{{range .GoFiles}}{{$$dir}}/{{.}}{{"\n"}}{{end}}')
VENDOR_LICENSES := $(shell ./vendor-licenses -src)

.PHONY: all test go-test clean

all: xq-api xq-api.8 NOTICE

test: go-test

NOTICE:
	./vendor-licenses -gen > NOTICE

go-test:
	go test -v -cover $(PACKAGE)/...

xq-api: $(GO_SRC)
	go build -mod=vendor -o "$@" -v $(PACKAGE)

xq-api.8: README.adoc
	asciidoctor --out-file="$@" -b manpage $<

clean:
	$(RM) xq-api xq-api.8 NOTICE
