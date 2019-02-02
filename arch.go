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
		glog.V(1).Infof("walking %s for repodata files", path)
		return a.loadDir(path)
	}
	return a.loadFile(path, "")
}

func (a *archIndex) loadDir(searchRoot string) error {
	searchRoot, err := filepath.Abs(searchRoot)
	if err != nil {
		return err
	}

	// Collect a list of -repodata files and load them.
	var files []string
	err = filepath.Walk(searchRoot, func(path string, wfi os.FileInfo, err error) error {
		if wfi.IsDir() {
			glog.V(2).Infof("walking %s for repodata files", path)
		} else if strings.HasSuffix(path, "-repodata") {
			files = append(files, path)
		}
		// TODO: Follow symlinks?
		return nil
	})
	if err != nil {
		return err
	}
	for _, path := range files {
		repo := repositoryFromFileSearchRoot(searchRoot, path)
		if err = a.loadFile(path, repo); err != nil {
			return err
		}
	}
	return nil
}

func (a *archIndex) loadFile(path, repo string) error {
	glog.Infof("loading %s", path)

	if !strings.HasSuffix(path, "-repodata") {
		return &os.PathError{
			Path: path,
			Op:   "read",
			Err:  errors.New("repodata files must end in -repodata"),
		}
	}

	if repo == "" {
		var ok bool
		repo, ok = repositoryFromPath(path)
		if !ok {
			glog.V(1).Infof("unable to determine repository for %s; defaulting to %s",
				path, defaultRepository)
			repo = defaultRepository
		}
	}

	arch := filepath.Base(strings.TrimSuffix(path, "-repodata"))
	rd := a.archs[arch]
	if rd == nil {
		rd = NewRepoData()
		a.archs[arch] = rd
	}

	packages := len(rd.Index())
	err := rd.LoadRepo(path, repo)
	if err != nil {
		return &os.PathError{Path: path, Err: err, Op: "load"}
	}

	packages = len(rd.Index()) - packages

	glog.V(1).Infof("loaded %s repo=%s new_packages=%d",
		path, repo, packages)

	return nil
}

func repositoryFromFileSearchRoot(searchRoot, path string) string {
	// Add compatibility check when using /var/db/xbps repodata
	if searchRoot == "/var/db/xbps" {
		return ""
	}
	repo := filepath.ToSlash(filepath.Dir(path))
	repo = strings.TrimPrefix(repo, searchRoot)
	repo = strings.TrimPrefix(repo, "/")
	repo = strings.TrimPrefix(repo, defaultRepository+"/")
	if repo == "" || repo == "/" || repo == "." {
		repo = defaultRepository
	}
	return repo
}

func repositoryFromPath(path string) (repo string, ok bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	dir := filepath.Dir(abs)

	// Look for foo/bar/current/path-a/path-b in path and pull out
	// everything from "current" and lower.
	repo, ok = repositoryFromPathList(dir)
	if !ok {
		repo, ok = repositoryFromBase(filepath.Base(dir))
	}
	return repo, ok
}

func repositoryFromBase(path string) (repo string, ok bool) {
	const currentSep = "_current_"
	if sep := strings.LastIndex(path, currentSep); sep > -1 {
		repo = path[sep+len(currentSep):]
		if repo != "" {
			return strings.Replace(repo, "_", "/", -1), true
		}
	}
	sep := strings.LastIndexByte(path, '_')
	if sep == -1 || sep >= len(path)-1 {
		return "", false
	}
	return path[sep+1:], true
}

func repositoryFromPathList(path string) (repo string, ok bool) {
	list := strings.Split(path, string(filepath.Separator))
	nth := -1
	for i := len(list) - 1; i >= 0; i-- {
		if list[i] == "current" {
			nth = i
			break
		}
	}
	if nth == -1 {
		return "", false
	}
	list = list[nth:]
	if len(list) > 1 { // drop "current" if there are more identifiers
		list = list[1:]
	}
	return strings.Join(list, "/"), true
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
