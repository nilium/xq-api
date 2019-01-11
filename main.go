package main // import "go.spiff.io/xq-api"

import (
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/golang/glog"
	"github.com/gorilla/handlers"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/sys/unix"
)

func main() {
	ec := 0
	defer func() { os.Exit(ec) }()
	exit := func(status int) {
		ec = status
		runtime.Goexit()
	}

	// Parse CLI arguments (including some implicit ones because glog defines some flags with
	// undesirable defaults).
	cli := flag.CommandLine
	var (
		network = cli.String("net", etos("XQAPI_LISTEN_NET", "tcp"),
			"listen network (unix, tcp, tcp4, tcp6)")
		listen = cli.String("listen", etos("XQAPI_LISTEN_ADDR", "127.0.0.1:8197"),
			"listen address")
		logAccess = cli.Bool("log-access", etob("XQAPI_LOG_ACCESS", false),
			"write access logs to stderr (info)")
		maxRunning = cli.Int("max-queries", etoi("XQAPI_MAX_QUERIES", 16),
			"the maximum number of filter queries to allow")
	)
	argv := append([]string{
		// Set by default to avoid creating files.
		// Can pass -logtostderr=false to override this.
		"-logtostderr",
		"-v=" + etos("XQAPI_LOG_VERBOSE", "0"),
	}, os.Args[1:]...)
	cli.Parse(argv)

	defer glog.Flush()

	api := NewQuerier(*maxRunning)
	sv := createServer(api, *logAccess)

	// Reload on boot to have data before the server starts (this isn't really strictly
	// necessary).
	if err := reloadRepoData(api, flag.Args()); err != nil {
		glog.Errorf("error loading initial repo data: %v", err)
		exit(1)
	}

	// Reload repodata on hup.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, unix.SIGHUP)
	go func() {
		for range sig {
			if err := reloadRepoData(api, flag.Args()); err != nil {
				glog.Warningf("Error reloading repository data: %v", err)
			}
		}
	}()

	// Start handling interrupt/terminate to die cleanly (mostly important for listening on
	// a unix socket).
	go func() {
		<-waitForSignal(unix.SIGINT, unix.SIGTERM)
		sv.Close()
	}()

	// Create listener.
	listener, err := net.Listen(*network, *listen)
	if err != nil {
		glog.Errorf("unable to listen on %s:%s: %v", *network, *listen, err)
		exit(1)
	}
	defer listener.Close()
	glog.Infof("listening: %v", listener.Addr())

	glog.Info("starting server")
	if err := sv.Serve(listener); err != nil && err != http.ErrServerClosed {
		glog.Errorf("server error: %v", err)
		exit(1)
	}
}

func createServer(api *Querier, logAccess bool) *http.Server {
	mux := httprouter.New()
	mux.GET("/v1/archs", api.Archs)
	mux.HEAD("/v1/archs", api.Archs)

	mux.GET("/v1/query/:arch", api.Query)
	mux.HEAD("/v1/query/:arch", api.Query)

	mux.GET("/v1/packages/:arch", api.PackageList)
	mux.HEAD("/v1/packages/:arch", api.PackageList)

	mux.GET("/v1/packages/:arch/:package", api.Package)
	mux.HEAD("/v1/packages/:arch/:package", api.Package)

	mux.NotFound = http.HandlerFunc(api.NotFound)

	zipper := gziphandler.GzipHandler(mux)

	cors := handlers.CORS(
		handlers.AllowedMethods([]string{"GET", "HEAD"}),
		handlers.AllowedHeaders([]string{
			"Accept-Encoding",
			"Accept",
			"Accept-Language",
			"Content-Language",
			"Origin",
			"If-None-Match",
		}),
	)(zipper)

	handler := http.Handler(cors)
	if logAccess {
		handler = AccessLog(handler)
	}

	return &http.Server{
		Handler: handler,
	}
}

func reloadRepoData(api *Querier, files []string) error {
	glog.Info("loading repodata...")
	archs, err := loadArchIndices(flag.Args())
	if err != nil {
		return err
	}
	packages := 0
	for _, rd := range archs.archs {
		packages += len(rd.Index())
	}
	glog.Infof("loaded repodata: read %d packages", packages)
	api.SetData(archs)
	return nil
}

type responseCodeCapture struct {
	Bytes int64
	Code  int
	set   bool
	http.ResponseWriter
}

func (r *responseCodeCapture) WriteHeader(code int) {
	r.ResponseWriter.WriteHeader(code)
	if r.set {
		return
	}
	r.set, r.Code = true, code
}

func (r *responseCodeCapture) Write(b []byte) (int, error) {
	if !r.set {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.Bytes += int64(n)
	return n, err
}

func (r *responseCodeCapture) WriteString(s string) (int, error) {
	type stringWriter interface {
		WriteString(string) (int, error)
	}
	if sw, ok := r.ResponseWriter.(stringWriter); ok {
		n, err := sw.WriteString(s)
		r.Bytes += int64(n)
		return n, err
	}
	return r.Write([]byte(s))
}

func AccessLog(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		rc := responseCodeCapture{ResponseWriter: w}
		t := time.Now()
		next.ServeHTTP(&rc, req)

		// Do not log 404s or 0s
		switch rc.Code {
		case 0, http.StatusNotFound, http.StatusNotModified:
			return
		}

		elapsed := time.Since(t).Seconds()
		addr := req.RemoteAddr
		if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
			addr += "," + xff
		}
		uri := strconv.QuoteToASCII(req.URL.RequestURI())
		uri = uri[1 : len(uri)-1]
		glog.Infof("%d\t%s\t%d\t%f\t%s\t%s",
			rc.Code,
			req.Method,
			rc.Bytes,
			elapsed,
			uri,
			addr,
		)
	}
}

func waitForSignal(signals ...os.Signal) <-chan os.Signal {
	out := make(chan os.Signal, 1) // Output channel
	go func() {
		sig := make(chan os.Signal, 1) // Buffer
		signal.Notify(sig, signals...)
		defer signal.Stop(sig)
		out <- (<-sig)
		close(out)
	}()
	return out
}
