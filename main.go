package main // import "go.spiff.io/xq-api"

import (
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/golang/glog"
	"github.com/gorilla/handlers"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/sys/unix"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8197", "listen address")
	logAccess := flag.Bool("log-access", false, "write access logs to stderr (info)")
	maxRunning := flag.Int("max-queries", 16, "the maximum number of filter queries to allow")
	flag.Parse()

	defer glog.Flush()
	glog.Infof("pid %d", os.Getpid())

	api := NewQuerier(*maxRunning)
	if err := reloadRepoData(api, flag.Args()); err != nil {
		glog.Fatalf("error loading initial repo data: %v", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, unix.SIGHUP)
	go func() {
		for range sig {
			if err := reloadRepoData(api, flag.Args()); err != nil {

			}
		}
	}()

	mux := httprouter.New()
	mux.GET("/v1/archs", api.Archs)
	mux.HEAD("/v1/archs", api.Archs)

	mux.GET("/v1/query/:arch", api.Query)
	mux.HEAD("/v1/query/:arch", api.Query)

	mux.GET("/v1/packages/:arch/", api.PackageList)
	mux.HEAD("/v1/packages/:arch/", api.PackageList)

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
	if *logAccess {
		handler = AccessLog(handler)
	}

	sv := &http.Server{
		Addr:    *listen,
		Handler: handler,
	}

	glog.Info("starting server")
	if err := sv.ListenAndServe(); err != nil {
		glog.Fatalf("server error: %v", err)
	}
}

func reloadRepoData(api *Querier, files []string) (err error) {
	glog.Info("loading repodata...")
	archs, err := loadArchIndices(flag.Args())
	if err != nil {
		return err
	}

	defer func() {
		if err == nil {
			return
		}
		for _, rd := range archs.archs {
			rd.Close()
		}
	}()

	packages := 0
	for _, rd := range archs.archs {
		packages += len(rd.Index())
	}
	glog.Infof("loaded repodata: read %d packages", packages)

	errch := make(chan error)
	for arch, rd := range archs.archs {
		go func(arch string, rd *RepoData) {
			t := time.Now()
			glog.Infof("indexing architecture %s (%d packages)...", arch, len(rd.Index()))
			var err error
			defer func() { errch <- err }()
			rd.bindex, err = rd.CreateSearchIndex()
			if err != nil {
				glog.Warningf("error indexing architecture %s (%d packages): %v", arch, len(rd.Index()), err)
				return
			}
			glog.Infof("finished indexing architecture %s (%d packages): %v", arch, len(rd.Index()), time.Since(t))
		}(arch, rd)
	}

	for range archs.archs {
		if e := <-errch; e != nil && err == nil {
			err = e
		}
	}

	if err != nil {
		return err
	}

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
