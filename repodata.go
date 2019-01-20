package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"
	"golang.org/x/tools/container/intsets"
	"howett.net/plist"
)

var etagEncoding = base64.RawURLEncoding

const repoIndexFile = "index.plist"
const defaultRepository = "current"

var errNoIndex = fmt.Errorf("index not found: %s", repoIndexFile)

type FilterFunc func(*packageData) bool

type packageIndex []*packageData

const (
	minSplitFilter   = 3000
	minIndexCapacity = 16
	splitSize        = 2000
)

func (ps packageIndex) Filter(fn FilterFunc) packageIndex {
	if len(ps) <= minSplitFilter {
		return ps.singleFilter(fn)
	}

	return ps.splitFilter(fn)
}

func (ps packageIndex) singleFilter(fn FilterFunc) packageIndex {
	ps2 := make(packageIndex, 0, 16)
	for _, p := range ps {
		if fn(p) {
			ps2 = append(ps2, p)
		}
	}
	return ps2
}

func (ps packageIndex) splitFilter(fn FilterFunc) packageIndex {
	var (
		want    int
		index   intsets.Sparse
		subsets = make(chan *intsets.Sparse)
	)

	for i := 0; i < len(ps); i += splitSize {
		min := i
		max := min + splitSize
		if max > len(ps) {
			max = len(ps)
		}
		want++
		set := ps[min:max]

		go func() {
			var sub intsets.Sparse
			for i, p := range set {
				if fn(p) {
					sub.Insert(min + i)
				}
			}
			subsets <- &sub
		}()
	}

	for ; want > 0; want-- {
		index.UnionWith(<-subsets)
	}

	ps2 := make(packageIndex, 0, index.Len())
	for i := 0; index.TakeMin(&i); {
		ps2 = append(ps2, ps[i])
	}

	return ps2
}

type RepoData struct {
	root      packageMap
	index     packageIndex
	nameIndex []string
	etag      string
}

func NewRepoData() *RepoData {
	return &RepoData{
		root: packageMap{},
	}
}

func (rd *RepoData) LoadRepo(path, repo string) error {
	fi, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fi.Close()

	return rd.ReadRepo(fi, repo)
}

func (rd *RepoData) Index() packageIndex {
	if rd == nil {
		return nil
	}
	return rd.index
}

func (rd *RepoData) NameIndex() []string {
	if rd == nil {
		return nil
	}
	return rd.nameIndex
}

func (rd *RepoData) ReadRepo(r io.Reader, repo string) error {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			return err
		}

		if hdr.Name == repoIndexFile {
			return rd.ReadRepoIndex(tr, repo)
		}
	}
	return errNoIndex
}

func copyToTempFile(r io.Reader) (*os.File, error) {
	tmpfile, err := ioutil.TempFile("", "index.plist")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(tmpfile.Name()); err != nil {
		tmpfile.Close()
		return nil, err
	}

	if _, err := io.Copy(tmpfile, r); err != nil {
		tmpfile.Close()
		return nil, err
	}

	return tmpfile, nil
}

func (rd *RepoData) ReadRepoIndex(r io.Reader, repo string) error {
	var err error
	rs, ok := r.(io.ReadSeeker)
	if !ok {
		var f *os.File
		if f, err = copyToTempFile(r); err != nil {
			return err
		}
		defer f.Close()
		rs = f
	}

	if repo == "" {
		repo = defaultRepository
	}

	pkg := packageMap{}
	err = plist.NewDecoder(rs).Decode(pkg)
	if err != nil {
		return err
	}

	// Merge indices and maps -- this gets around a flaw in howett.net/plist where decoding into
	// an existing dataset will result in an invalid use of the reflect package and panic.
	index := rd.index
	for k, p := range pkg {
		old, ok := rd.root[k]
		if p.Name == "" {
			p.Name = k
			_, p.Version, p.Revision, _ = ParseVersionedName(p.PackageVersion)
		}

		p.Repository = repo

		// Do naive case normalization for searches -- shouldn't have an impact on these
		// given that everything in repodata is currently ASCII.
		p.SearchPackageVersion = strings.ToLower(p.PackageVersion)
		p.SearchShortDesc = strings.ToLower(p.ShortDesc)

		p.ETag, err = p.computeETag()
		if err != nil {
			// This really shouldn't happen -- it would mean JSON encoding of packages
			// was broken.
			glog.Errorf("unable to compute etag for %q: %v", p.PackageVersion, err)
			return err
		}

		rd.root[k] = p
		if ok {
			index[old.Index] = p
		} else {
			index = append(index, p)
		}
	}

	sort.Slice(index, func(i, j int) bool {
		return index[i].Name < index[j].Name
	})

	for i, p := range index {
		p.Index = i
	}

	rd.index = index

	names := rd.nameIndex[:0]
	for _, p := range rd.index {
		names = append(names, p.Name)
	}
	rd.nameIndex = names

	etag, err := rd.computeETag()
	if err != nil {
		return err
	}
	rd.etag = etag

	return nil
}

func (rd *RepoData) Package(name string) *packageData {
	if rd == nil {
		return nil
	}
	return rd.root[name]
}

func (rd *RepoData) computeETag() (string, error) {
	h := sha1.New()

	index := rd.Index()
	if err := binary.Write(h, binary.LittleEndian, int64(len(index))); err != nil {
		return "", err
	}

	for _, p := range index {
		binary.Write(h, binary.LittleEndian, int64(len(p.PackageVersion)+len(p.ETag)))
		io.WriteString(h, p.PackageVersion)
		io.WriteString(h, p.ETag)
	}

	sum := h.Sum(make([]byte, 0, h.Size()))
	etag := `W/"` + etagEncoding.EncodeToString(sum) + `"`
	return etag, nil
}

func (rd *RepoData) ETag() string {
	return rd.etag
}

var errNoRevision = errors.New("revision not found")
var errNoVersion = errors.New("version not found")

func ParseVersionedName(s string) (name, version string, revision int, err error) {
	if s == "" {
		err = errors.New("name is empty")
		return
	}

	revidx := strings.LastIndexByte(s, '_')
	if revidx == -1 {
		err = errNoRevision
		return
	}
	vsnidx := strings.LastIndexByte(s[:revidx], '-')
	if vsnidx == -1 {
		err = errNoVersion
		return
	}

	revision, err = strconv.Atoi(s[revidx+1:])
	if err != nil {
		return
	}

	name, version = s[:vsnidx], s[vsnidx+1:revidx]
	return
}

type urlVal url.URL

func (u *urlVal) UnmarshalText(p []byte) error {
	uu, err := url.Parse(string(p))
	if err != nil {
		return err
	}
	*u = urlVal(*uu)
	return nil
}

func (u *urlVal) MarshalText() ([]byte, error) {
	if u == nil {
		return nil, nil
	}

	return []byte((*url.URL)(u).String()), nil
}

type timeVal time.Time

func (t *timeVal) MarshalText() ([]byte, error) {
	return []byte(time.Time(*t).Format(time.RFC3339)), nil
}

func (t *timeVal) UnmarshalText(p []byte) error {
	const layout = `2006-01-02 15:04 MST`
	tt, err := time.ParseInLocation(layout, string(p), time.UTC)
	if err != nil {
		return err
	}
	*t = timeVal(tt.UTC())
	return nil
}

type packageMap map[string]*packageData

type packageData struct {
	PackageVersion string `plist:"pkgver" json:"-"`
	Name           string `plist:"-" json:"name,omitempty"`
	Version        string `plist:"-" json:"version,omitempty"`
	Revision       int    `plist:"-" json:"revision,omitempty"`

	Repository     string  `plist:"-" json:"repository,omitempty"`
	Architecture   string  `plist:"architecture" json:"architecture,omitempty"`
	BuildDate      timeVal `plist:"build-date" json:"build_date,omitempty"`
	BuildOptions   string  `plist:"build-options" json:"build_options,omitempty"`
	FilenameSHA256 string  `plist:"filename-sha256" json:"filename_sha256,omitempty"`
	FilenameSize   int64   `plist:"filename-size" json:"filename_size,omitempty"`
	Homepage       *urlVal `plist:"homepage" json:"homepage,omitempty"`
	InstalledSize  int64   `plist:"installed_size" json:"installed_size,omitempty"`
	License        string  `plist:"license" json:"license,omitempty"`
	Maintainer     string  `plist:"maintainer" json:"maintainer,omitempty"`
	ShortDesc      string  `plist:"short_desc" json:"short_desc,omitempty"`
	Preserve       bool    `plist:"preserve" json:"preserve,omitempty"`

	SourceRevisions string `plist:"source-revisions" json:"source_revisions,omitempty"`

	RunDepends []string `plist:"run_depends" json:"run_depends,omitempty"`

	ShlibRequires []string `plist:"shlib-requires" json:"shlib_requires,omitempty"`
	ShlibProvides []string `plist:"shlib-provides" json:"shlib_provides,omitempty"`

	Conflicts []string `plist:"conflicts" json:"conflicts,omitempty"`
	Reverts   []string `plist:"reverts" json:"reverts,omitempty"`

	Replaces     []string            `plist:"replaces" json:"replaces,omitempty"`
	Alternatives map[string][]string `plist:"alternatives" json:"alternatives,omitempty"`

	ConfFiles []string `plist:"conf_files" json:"conf_files,omitempty"`

	// Lower-case versions of PackageVersion and ShortDesc for searching
	SearchPackageVersion string `plist:"-" json:"-"`
	SearchShortDesc      string `plist:"-" json:"-"`

	Index int    `plist:"-" json:"-"`
	ETag  string `plist:"-" json:"-"`
}

func (p *packageData) computeETag() (string, error) {
	h := sha1.New()
	if err := json.NewEncoder(h).Encode(p); err != nil {
		return "", nil
	}
	sum := h.Sum(make([]byte, 0, h.Size()))
	etag := `W/"` + etagEncoding.EncodeToString(sum) + `"`
	return etag, nil
}
