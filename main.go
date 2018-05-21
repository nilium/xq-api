package main // import "go.spiff.io/xq-api"

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/golang/glog"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/sys/unix"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8197", "listen address")
	maxRunning := flag.Int("max-queries", 16, "the maximum number of filter queries to allow")
	flag.Parse()

	defer glog.Flush()
	glog.Infof("pid %d", os.Getpid())

	runner := NewRunner(*maxRunning)
	if err := reloadRepoData(runner, flag.Args()); err != nil {
		glog.Fatalf("error loading initial repo data: %v", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, unix.SIGHUP)
	go func() {
		for range sig {
			if err := reloadRepoData(runner, flag.Args()); err != nil {

			}
		}
	}()

	mux := httprouter.New()
	mux.GET("/v1/query/:arch", runner.Query)
	mux.GET("/v1/package/:arch/:package", runner.Package)
	mux.NotFound = http.DefaultServeMux

	sv := &http.Server{
		Addr:    *listen,
		Handler: mux,
	}

	glog.Info("starting server")
	if err := sv.ListenAndServe(); err != nil {
		glog.Fatalf("server error: %v", err)
	}
}

type archIndex map[string]*RepoData

func loadArchIndices(files []string) (archIndex, error) {
	archs := archIndex{}

	for _, rdpath := range files {
		fi, err := os.Stat(rdpath)
		if err != nil {
			glog.Warningf("ignoring repodata path because stat failed: %v", err)
			continue
		}

		if fi.IsDir() {
			err = archs.loadDir(rdpath)
		} else {
			err = archs.loadFile(rdpath)
		}

		if err != nil {
			return nil, err
		}
	}

	return archs, nil
}

func (a archIndex) loadDir(path string) error {
	var files []string
	err := filepath.Walk(path, func(path string, wfi os.FileInfo, err error) error {
		if !wfi.IsDir() && strings.HasSuffix(path, "-repodata") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, rdpath := range files {
		if err = a.loadFile(rdpath); err != nil {
			return err
		}
	}
	return nil
}

func (a archIndex) loadFile(path string) error {
	glog.Infof("loading %s", path)

	if !strings.HasSuffix(path, "-repodata") {
		return &os.PathError{
			Path: path,
			Op:   "read",
			Err:  errors.New("repodata files must end in -repodata"),
		}
	}

	arch := filepath.Base(strings.TrimSuffix(path, "-repodata"))
	rd := a[arch]
	if rd == nil {
		rd = NewRepoData()
		a[arch] = rd
	}

	err := rd.LoadRepo(path)
	if err != nil {
		return &os.PathError{Path: path, Err: err, Op: "load"}
	}

	return nil
}

type Runner struct {
	data atomic.Value  // Run, SetData
	sema chan struct{} // Run
}

func NewRunner(maxProcs int) *Runner {
	if maxProcs < 1 {
		maxProcs = 1
	}

	runner := &Runner{
		sema: make(chan struct{}, maxProcs),
	}
	runner.SetData(archIndex{})
	return runner
}

func (r *Runner) SetData(index archIndex) {
	if index != nil {
		r.data.Store(index)
	}
}

func (r *Runner) getData() archIndex {
	return r.data.Load().(archIndex)
}

func (r *Runner) reply(w http.ResponseWriter, val interface{}) {
	var buf bytes.Buffer
	switch err := json.NewEncoder(&buf).Encode(val).(type) {
	case nil:
	default:
		glog.Warningf("unable to encode package result: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err := buf.WriteTo(w)
	switch err.(type) {
	case nil:
	case net.Error:
		// Don't care about network errors
	default:
		glog.Warningf("unexpected error writing response: %v", err)
	}
}

func (r *Runner) Package(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
	arch := params.ByName("arch")
	pkgname := params.ByName("package")

	rd, ok := r.getData()[arch]
	if !ok {
		http.NotFound(w, req)
		return
	}

	pkg, ok := rd.root[pkgname]
	if !ok {
		http.NotFound(w, req)
		return
	}

	response := struct {
		Data *packageData `json:"data"`
	}{
		Data: pkg,
	}

	r.reply(w, response)
}

func (r *Runner) Query(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
	r.sema <- struct{}{}
	defer func() { <-r.sema }()

	query := strings.ToLower(req.FormValue("q"))
	arch := params.ByName("arch")
	rd, ok := r.getData()[arch]

	if !ok {
		http.NotFound(w, req)
		return
	}

	sub := rd.Index().Filter(func(p *packageData) bool {
		return strings.Contains(strings.ToLower(p.PackageVersion), query) ||
			strings.Contains(strings.ToLower(p.ShortDesc), query)
	})

	type shortEntry struct {
		Name     string `json:"name"`
		Version  string `json:"version"`
		Revision int    `json:"revision"`
		Desc     string `json:"desc,omitempty"`
	}

	response := struct {
		Data []shortEntry `json:"data"`
	}{
		Data: make([]shortEntry, len(sub)),
	}

	for i, p := range sub {
		response.Data[i] = shortEntry{
			Name:     p.Name,
			Version:  p.Version,
			Revision: p.Revision,
			Desc:     p.ShortDesc,
		}
	}

	r.reply(w, response)
}

func reloadRepoData(runner *Runner, files []string) error {
	glog.Info("loading repodata...")
	archs, err := loadArchIndices(flag.Args())
	if err != nil {
		return err
	}
	packages := 0
	for _, rd := range archs {
		packages += len(rd.Index())
	}
	glog.Infof("loaded repodata: read %d packages", packages)
	runner.SetData(archs)
	return nil
}
