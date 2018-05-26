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

	"github.com/blevesearch/bleve"
	"github.com/blevesearch/bleve/analysis/analyzer/keyword"
	"github.com/blevesearch/bleve/analysis/analyzer/web"
	"github.com/blevesearch/bleve/mapping"
	"github.com/golang/glog"
	"golang.org/x/tools/container/intsets"
	"howett.net/plist"
)

var etagEncoding = base64.RawURLEncoding

const repoIndexFile = "index.plist"

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
	bindex    bleve.Index
}

func NewRepoData() *RepoData {
	return &RepoData{
		root: packageMap{},
	}
}

func (rd *RepoData) Close() error {
	if rd == nil || rd.bindex == nil {
		return nil
	}
	return rd.bindex.Close()
}

func (rd *RepoData) LoadRepo(path string) error {
	fi, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fi.Close()

	return rd.ReadRepo(fi)
}

func newPackageMapping() *mapping.DocumentMapping {
	allmap := bleve.NewTextFieldMapping()
	allmap.Analyzer = "en"
	allmap.IncludeInAll = true
	allmap.Store = false

	unimap := bleve.NewTextFieldMapping()
	unimap.Store = false
	unimap.IncludeInAll = true

	kwallmap := bleve.NewTextFieldMapping()
	kwallmap.Analyzer = keyword.Name
	kwallmap.IncludeInAll = true
	kwallmap.Store = false

	textmap := bleve.NewTextFieldMapping()
	textmap.Analyzer = "en"
	textmap.IncludeInAll = false
	textmap.Store = false

	webmap := bleve.NewTextFieldMapping()
	webmap.Analyzer = web.Name
	webmap.IncludeInAll = false
	webmap.Store = false

	kwmap := bleve.NewTextFieldMapping()
	kwmap.Analyzer = keyword.Name
	kwmap.IncludeInAll = false
	kwmap.Store = false

	nummap := bleve.NewNumericFieldMapping()
	nummap.IncludeInAll = false
	nummap.Store = false

	datemap := bleve.NewDateTimeFieldMapping()
	// datemap.DateFormat = time.RFC3339
	datemap.IncludeInAll = false
	datemap.Store = false

	boolmap := bleve.NewBooleanFieldMapping()
	boolmap.IncludeInAll = false
	boolmap.Store = false

	pkgmap := bleve.NewDocumentMapping()
	pkgmap.StructTagKey = "search"

	// Keep name, pkgver, and desc in the _all index
	pkgmap.AddFieldMappingsAt("name", kwallmap, allmap, unimap)
	pkgmap.AddFieldMappingsAt("pkgver", kwallmap, allmap, unimap)
	pkgmap.AddFieldMappingsAt("desc", allmap, unimap)

	pkgmap.AddFieldMappingsAt("arch", kwmap, textmap, webmap)
	pkgmap.AddFieldMappingsAt("version", kwmap)
	pkgmap.AddFieldMappingsAt("revision", nummap)
	pkgmap.AddFieldMappingsAt("builton", datemap)
	pkgmap.AddFieldMappingsAt("buildopts", kwmap, textmap, webmap)
	pkgmap.AddFieldMappingsAt("homepage", kwmap, textmap, webmap)
	pkgmap.AddFieldMappingsAt("license", kwmap, textmap, webmap)
	pkgmap.AddFieldMappingsAt("maintainer", textmap, webmap)
	pkgmap.AddFieldMappingsAt("preserve", boolmap)
	pkgmap.AddFieldMappingsAt("depends", kwmap, textmap, webmap)
	pkgmap.AddFieldMappingsAt("shlibreq", kwmap, textmap, webmap)
	pkgmap.AddFieldMappingsAt("shlib", kwmap, textmap, webmap)
	pkgmap.AddFieldMappingsAt("conflicts", kwmap, textmap, webmap)
	pkgmap.AddFieldMappingsAt("reverts", kwmap, textmap, webmap)
	pkgmap.AddFieldMappingsAt("replaces", kwmap, textmap, webmap)
	pkgmap.AddFieldMappingsAt("conffiles", kwmap, textmap, webmap)
	pkgmap.AddFieldMappingsAt("etag", kwmap)

	return pkgmap
}

func (rd *RepoData) CreateSearchIndex() (index bleve.Index, err error) {
	mapping := bleve.NewIndexMapping()
	mapping.AddDocumentMapping("package", newPackageMapping())

	index, err = bleve.NewMemOnly(mapping)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			index.Close()
			index = nil
		}
	}()

	if len(rd.Index()) == 0 {
		return index, nil
	}

	const batchSize = 1000
	arch := rd.Index()[0].Architecture
	total, size, count := 0, len(rd.Index()), 0
	batch := index.NewBatch()
	t := time.Now()
	for _, pkg := range rd.Index() {
		if err = batch.Index(pkg.Name, pkg); err != nil {
			return nil, err
		}

		if count++; count >= batchSize {
			if err = index.Batch(batch); err != nil {
				return nil, err
			}
			elapsed := time.Since(t)
			total += count
			glog.Infof("indexed %d packages (%d/%d) in architecture %s (%v)", count, total, size, arch, elapsed)

			batch = index.NewBatch()
			count = 0
			t = time.Now()
		}
	}

	if count == 0 {
	} else if err = index.Batch(batch); err == nil {
		elapsed := time.Since(t)
		total += count
		glog.Infof("indexed %d packages (%d/%d) in architecture %s (%v)", count, total, size, arch, elapsed)
	}

	return index, err
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

func (rd *RepoData) ReadRepo(r io.Reader) error {
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
			return rd.ReadRepoIndex(tr)
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

func (rd *RepoData) ReadRepoIndex(r io.Reader) error {
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

		p.ETag, err = p.computeETag()
		if err != nil {
			// This really shouldn't happen -- it would mean JSON encoding of packages
			// was broken.
			glog.Errorf("unable to compute etag for %q: %v", p.PackageVersion, err)
			return err
		}
		p.HTTPETag = `"` + p.ETag + `"`

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
	etag := `"` + etagEncoding.EncodeToString(sum) + `"`
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
	PackageVersion string `plist:"pkgver" json:"-" search:"pkgver"`
	Name           string `plist:"-" json:"name,omitempty" search:"name"`
	Version        string `plist:"-" json:"version,omitempty" search:"version"`
	Revision       int    `plist:"-" json:"revision,omitempty" search:"revision"`

	Architecture   string  `plist:"architecture" json:"architecture,omitempty" search:"arch"`
	BuildDate      timeVal `plist:"build-date" json:"build_date,omitempty" search:"-"`
	BuildDateStr   string  `plist:"-" json:"-" search:"builton"`
	BuildOptions   string  `plist:"build-options" json:"build_options,omitempty" search:"buildopts"`
	FilenameSHA256 string  `plist:"filename-sha256" json:"filename_sha256,omitempty" search:"-"`
	FilenameSize   int64   `plist:"filename-size" json:"filename_size,omitempty" search:"-"`
	Homepage       *urlVal `plist:"homepage" json:"homepage,omitempty" search:"-"`
	HomepageStr    string  `plist:"-" json:"-" search:"homepage"`
	InstalledSize  int64   `plist:"installed_size" json:"installed_size,omitempty" search:"-"`
	License        string  `plist:"license" json:"license,omitempty" search:"license"`
	Maintainer     string  `plist:"maintainer" json:"maintainer,omitempty" search:"maintainer"`
	ShortDesc      string  `plist:"short_desc" json:"short_desc,omitempty" search:"desc"`
	Preserve       bool    `plist:"preserve" json:"preserve,omitempty" search:"preserve"`

	SourceRevisions string `plist:"source-revisions" json:"source_revisions,omitempty" search:"-"`

	RunDepends []string `plist:"run_depends" json:"run_depends,omitempty" search:"depends"`

	ShlibRequires []string `plist:"shlib-requires" json:"shlib_requires,omitempty" search:"shlibreq"`
	ShlibProvides []string `plist:"shlib-provides" json:"shlib_provides,omitempty" search:"shlib"`

	Conflicts []string `plist:"conflicts" json:"conflicts,omitempty" search:"conflicts"`
	Reverts   []string `plist:"reverts" json:"reverts,omitempty" search:"reverts"`

	Replaces     []string            `plist:"replaces" json:"replaces,omitempty" search:"replaces"`
	Alternatives map[string][]string `plist:"alternatives" json:"alternatives,omitempty" search:"-"`

	ConfFiles []string `plist:"conf_files" json:"conf_files,omitempty" search:"conffiles"`

	Index    int    `plist:"-" json:"-" search:"-"`
	ETag     string `plist:"-" json:"etag" search:"etag"`
	HTTPETag string `plist:"-" json:"-" search:"-"`
}

func (p *packageData) Type() string {
	return "package"
}

var _ mapping.Classifier = (*packageData)(nil)

func (p *packageData) computeETag() (string, error) {
	h := sha1.New()
	if err := json.NewEncoder(h).Encode(p); err != nil {
		return "", nil
	}
	sum := h.Sum(make([]byte, 0, h.Size()))
	etag := etagEncoding.EncodeToString(sum)
	return etag, nil
}
