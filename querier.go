package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/golang/glog"
	"github.com/julienschmidt/httprouter"
)

type Querier struct {
	data atomic.Value  // Handlers / SetData
	sema chan struct{} // Limited handlers
}

func NewQuerier(maxProcs int) *Querier {
	if maxProcs < 1 {
		maxProcs = 1
	}

	querier := &Querier{
		sema: make(chan struct{}, maxProcs),
	}
	querier.SetData(new(archIndex))
	return querier
}

func (qr *Querier) SetData(index *archIndex) {
	if index != nil {
		qr.data.Store(index)
	}
}

func (qr *Querier) getData() *archIndex {
	return qr.data.Load().(*archIndex)
}

func (qr *Querier) reply(w http.ResponseWriter, code int, val interface{}) {
	// Currently just request browsers cache all responses for five minutes at most. It doesn't
	// matter if the cached value is a few minutes old when dealing with search-able repodata
	// from the browser.
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "application/json")

	// Write an empty response
	if val == nil {
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(code)
		return
	}

	// Encode response, write headers, then write body
	var buf bytes.Buffer
	switch err := json.NewEncoder(&buf).Encode(val).(type) {
	case nil:
	default:
		glog.Warningf("unable to encode package result: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(code)

	_, err := buf.WriteTo(w)
	switch err.(type) {
	case nil:
	case net.Error:
		// Don't care about network errors
	default:
		glog.Warningf("unexpected error writing response: %v", err)
	}
}

func (qr *Querier) skipIfMatch(w http.ResponseWriter, req *http.Request, etag string) bool {
	if etag == "" {
		return false
	}

	w.Header().Set("Etag", etag)
	if cacheTag := req.Header.Get("If-None-Match"); cacheTag == etag {
		qr.reply(w, http.StatusNotModified, nil)
		return true
	}

	return false
}

func (qr *Querier) NotFound(w http.ResponseWriter, _ *http.Request) {
	qr.reply(w, http.StatusNotFound, struct{}{})
}

func (qr *Querier) Archs(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
	root := qr.getData()
	if qr.skipIfMatch(w, req, root.IndexETag()) {
		return
	}

	if req.Method == "HEAD" {
		qr.reply(w, http.StatusOK, nil)
		return
	}

	type archEntry struct {
		Name         string `json:"name"`
		PackagesPath string `json:"packages_path"`
		QueryPath    string `json:"query_path"`
	}

	index := root.Index()
	response := struct {
		Data []string `json:"data"`
	}{
		Data: index,
	}

	qr.reply(w, http.StatusOK, response)
}

func (qr *Querier) PackageList(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
	arch := params.ByName("arch")
	rd := qr.getData().Arch(arch)
	if rd == nil {
		qr.NotFound(w, req)
		return
	}

	if qr.skipIfMatch(w, req, rd.ETag()) {
		return
	}

	if req.Method == "HEAD" {
		qr.reply(w, http.StatusOK, nil)
		return
	}

	response := struct {
		Data []string `json:"data"`
	}{
		Data: rd.NameIndex(),
	}

	qr.reply(w, http.StatusOK, response)
}

func (qr *Querier) Package(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
	arch := params.ByName("arch")
	pkgname := params.ByName("package")

	rd := qr.getData().Arch(arch)
	if rd == nil {
		qr.NotFound(w, req)
		return
	}

	pkg := rd.Package(pkgname)
	if pkg == nil {
		qr.NotFound(w, req)
		return
	}

	if qr.skipIfMatch(w, req, pkg.ETag) {
		return
	}

	if req.Method == "HEAD" {
		qr.reply(w, http.StatusOK, nil)
		return
	}

	response := struct {
		Data *packageData `json:"data"`
	}{
		Data: pkg,
	}

	qr.reply(w, http.StatusOK, response)
}

func (qr *Querier) Query(w http.ResponseWriter, req *http.Request, params httprouter.Params) {

	query := strings.ToLower(req.FormValue("q"))
	arch := params.ByName("arch")
	rd := qr.getData().Arch(arch)
	if rd == nil {
		qr.NotFound(w, req)
		return
	}

	if qr.skipIfMatch(w, req, rd.ETag()) {
		return
	}

	if req.Method == "HEAD" {
		qr.reply(w, http.StatusOK, nil)
		return
	}

	qr.sema <- struct{}{}
	defer func() { <-qr.sema }()
	sub := rd.Index()
	if query != "" {
		sub = sub.Filter(func(p *packageData) bool {
			return strings.Contains(p.SearchPackageVersion, query) ||
				strings.Contains(p.SearchShortDesc, query)
		})
	}

	type shortEntry struct {
		Name      string `json:"name"`
		Version   string `json:"version"`
		Revision  int    `json:"revision"`
		ShortDesc string `json:"short_desc,omitempty"`
	}

	response := struct {
		Data []shortEntry `json:"data"`
	}{
		Data: make([]shortEntry, len(sub)),
	}

	for i, p := range sub {
		response.Data[i] = shortEntry{
			Name:      p.Name,
			Version:   p.Version,
			Revision:  p.Revision,
			ShortDesc: p.ShortDesc,
		}
	}

	qr.reply(w, http.StatusOK, response)
}
