package main

import (
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/golang/glog"
)

// archIndex is a map of architecture identifiers (e.g., "x86_64") to
// parsed repodata.
type archIndex struct {
	archs map[string]*RepoData

	// Index listing
	names []string
	etag  string
}

func (a *archIndex) Arch(name string) *RepoData {
	if a == nil {
		return nil
	}
	return a.archs[name]
}

func (a *archIndex) Index() []string {
	if a == nil {
		return nil
	}
	return a.names
}

func (a *archIndex) IndexETag() string {
	if a == nil {
		return ""
	}
	return a.etag
}

// loadArchIndices loads architecture-specific repodata into an
// archIndex and returns the resulting index.
//
// files is a list of files ending in -repodata or directories to be
// walked in search of -repodata files.
//
// A repodata file is of the form <arch>-repodata. So, x86_64-repodata
// is for the arch x86_64.
func loadArchIndices(files []string) (*archIndex, error) {
	archs := &archIndex{
		archs: map[string]*RepoData{},
		names: []string{},
	}

	for _, rdpath := range files {
		err := archs.loadPath(rdpath)
		if err != nil {
			return nil, err
		}
	}

	return archs, archs.init()
}

func (a *archIndex) loadPath(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		glog.Warningf("ignoring repodata path: stat failed: %v", err)
		return nil
	}

	if fi.IsDir() {
		return a.loadDir(path)
	}
	return a.loadFile(path)
}

func (a *archIndex) loadDir(path string) error {
	// Collect a list of -repodata files and load them.
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
	for _, path = range files {
		if err = a.loadFile(path); err != nil {
			return err
		}
	}
	return nil
}

func (a *archIndex) loadFile(path string) error {
	glog.Infof("loading %s", path)

	if !strings.HasSuffix(path, "-repodata") {
		return &os.PathError{
			Path: path,
			Op:   "read",
			Err:  errors.New("repodata files must end in -repodata"),
		}
	}

	arch := filepath.Base(strings.TrimSuffix(path, "-repodata"))
	rd := a.archs[arch]
	if rd == nil {
		rd = NewRepoData()
		a.archs[arch] = rd
	}

	err := rd.LoadRepo(path)
	if err != nil {
		return &os.PathError{Path: path, Err: err, Op: "load"}
	}

	return nil
}

func (a *archIndex) computeETag() string {
	h := sha1.New()
	binary.Write(h, binary.LittleEndian, int64(len(a.names)))
	for _, name := range a.names {
		binary.Write(h, binary.LittleEndian, int64(len(name)))
		io.WriteString(h, name)
	}
	sum := h.Sum(make([]byte, 0, h.Size()))
	return `W/"` + etagEncoding.EncodeToString(sum) + `"`
}

func (a *archIndex) init() error {
	names := make([]string, 0, len(a.archs))
	for k := range a.archs {
		names = append(names, k)
	}
	sort.Strings(names)

	a.names = names
	a.etag = a.computeETag()
	return nil
}