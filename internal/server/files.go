package server

// /files moves files between the client machine and this host over plain
// HTTP, so they do not travel through tool calls (and the model's context)
// as text. It sits behind the same token and IP allowlist as /mcp, paths go
// through the same path guard as the file tools, and every transfer lands in
// the operation log, the audit log and the history like a tool call.
//
//	PUT /files?path=<file>[&overwrite=1][&mkdirs=1][&sha256=<hex>]   body: the file
//	PUT /files?path=<dir>&extract=tgz[&overwrite=1][&mkdirs=1]        body: a .tar.gz
//	GET /files?path=<file>                                            the file
//	GET /files?path=<dir>[&exclude=.git,node_modules]                 a .tar.gz of it
//
// Uploads are written to a temporary name first and only renamed into place
// when the whole body arrived (and matches sha256 when given). Replies carry
// the size and sha256; file_info gives the same for files already here.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	// maxUploadBytes caps one upload body.
	maxUploadBytes = 8 << 30
	// fileInfoMaxPaths caps the paths of one file_info call.
	fileInfoMaxPaths = 50
	// readFilesMax caps the files of one read_files call.
	readFilesMax = 20
)

// transferIdle ends a transfer that makes no progress for this long, so a
// client that stalls or vanished without closing the connection does not
// keep a request (and a half-written temporary file) around. Tests shorten it.
var transferIdle = 2 * time.Minute

// idleReader moves the connection's read deadline forward before each read.
type idleReader struct {
	r  io.Reader
	rc *http.ResponseController
}

func (i *idleReader) Read(p []byte) (int, error) {
	_ = i.rc.SetReadDeadline(time.Now().Add(transferIdle))
	return i.r.Read(p)
}

// idleWriter moves the connection's write deadline forward before each write.
type idleWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (i *idleWriter) Write(p []byte) (int, error) {
	_ = i.rc.SetWriteDeadline(time.Now().Add(transferIdle))
	return i.w.Write(p)
}

type fileUploadIn struct {
	Path      string `json:"path"`
	Overwrite bool   `json:"overwrite,omitempty"`
	Mkdirs    bool   `json:"mkdirs,omitempty"`
	Extract   string `json:"extract,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

type fileDownloadIn struct {
	Path    string `json:"path"`
	Exclude string `json:"exclude,omitempty"`
}

// uploadResult is the JSON reply of an upload.
type uploadResult struct {
	Path      string   `json:"path"`
	Bytes     int64    `json:"bytes"`
	SHA256    string   `json:"sha256"`
	Overwrote bool     `json:"overwrote,omitempty"`
	Files     int      `json:"files,omitempty"`
	Skipped   []string `json:"skipped,omitempty"`
}

func (r *uploadResult) summary() string {
	if r.Files > 0 {
		s := fmt.Sprintf("extracted %d files (%d bytes) into %s, archive sha256 %s", r.Files, r.Bytes, r.Path, r.SHA256)
		if len(r.Skipped) > 0 {
			s += fmt.Sprintf(", skipped %d non-regular entries", len(r.Skipped))
		}
		return s
	}
	verb := "created"
	if r.Overwrote {
		verb = "overwrote"
	}
	return fmt.Sprintf("%s %s (%d bytes, sha256 %s)", verb, r.Path, r.Bytes, r.SHA256)
}

// httpError carries the status code an upload or download failure answers with.
type httpError struct {
	code int
	err  error
}

func (e *httpError) Error() string { return e.err.Error() }

func failf(code int, format string, args ...any) error {
	return &httpError{code: code, err: fmt.Errorf(format, args...)}
}

func statusOf(err error) int {
	var he *httpError
	if errors.As(err, &he) {
		return he.code
	}
	if errors.Is(err, fs.ErrNotExist) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func queryFlag(q url.Values, name string) bool {
	switch strings.ToLower(q.Get(name)) {
	case "1", "true", "yes", "y":
		return true
	}
	return false
}

// filesHandler serves /files; Handler puts authentication in front.
func (s *Server) filesHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.Method {
		case http.MethodPut, http.MethodPost:
			in := fileUploadIn{
				Path: q.Get("path"), Overwrite: queryFlag(q, "overwrite"), Mkdirs: queryFlag(q, "mkdirs"),
				Extract: strings.ToLower(q.Get("extract")), SHA256: strings.ToLower(strings.TrimSpace(q.Get("sha256"))),
			}
			s.trackHTTP(r.Context(), "file_upload", in, func() (string, error) {
				res, err := s.upload(w, r, in)
				if err != nil {
					writeJSONStatus(w, statusOf(err), map[string]string{"error": err.Error()})
					return "", err
				}
				writeJSONStatus(w, http.StatusOK, res)
				return res.summary(), nil
			})
		case http.MethodGet, http.MethodHead:
			in := fileDownloadIn{Path: q.Get("path"), Exclude: q.Get("exclude")}
			s.trackHTTP(r.Context(), "file_download", in, func() (string, error) {
				out, err := s.download(w, r, in)
				if err != nil && !errors.Is(err, errStreamStarted) {
					http.Error(w, err.Error(), statusOf(err))
				}
				return out, err
			})
		default:
			w.Header().Set("Allow", "GET, HEAD, PUT, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// trackHTTP logs, audits and records a /files request like addTool does
// for a tool call.
func (s *Server) trackHTTP(ctx context.Context, name string, in any, fn func() (string, error)) {
	start := time.Now()
	log.Printf("[操作开始] %s (%s) %s", toolLabel(name), name, describeArgs(in))
	out, err := fn()
	elapsed := time.Since(start)
	s.logCall(name, in, out, err, elapsed)
	s.record(ctx, name, in, out, err, start, elapsed)
	ev := map[string]any{
		"event":       "tool",
		"tool":        name,
		"args":        summarize(in),
		"duration_ms": elapsed.Milliseconds(),
		"ok":          err == nil,
	}
	if err != nil {
		ev["error"] = err.Error()
	}
	s.auditEvent(ev)
}

// ---------- upload ----------

func (s *Server) upload(w http.ResponseWriter, r *http.Request, in fileUploadIn) (*uploadResult, error) {
	if strings.TrimSpace(in.Path) == "" {
		return nil, failf(http.StatusBadRequest, "path is required")
	}
	p, err := s.guard.Resolve(in.Path)
	if err != nil {
		return nil, &httpError{code: http.StatusForbidden, err: err}
	}
	limited := http.MaxBytesReader(w, r.Body, maxUploadBytes)
	defer limited.Close()
	body := &idleReader{r: limited, rc: http.NewResponseController(w)}
	switch in.Extract {
	case "":
		return uploadFile(p, body, in)
	case "tgz", "tar.gz":
		return uploadTree(p, body, in)
	default:
		return nil, failf(http.StatusBadRequest, "unknown extract %q (use tgz)", in.Extract)
	}
}

// ensureDir makes sure dir exists, creating it only when mkdirs is set.
func ensureDir(dir string, mkdirs bool) error {
	st, err := os.Stat(dir)
	switch {
	case err == nil && st.IsDir():
		return nil
	case err == nil:
		return failf(http.StatusConflict, "%s is not a directory", dir)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	case !mkdirs:
		return failf(http.StatusConflict, "directory %s does not exist; pass mkdirs=1 to create it", dir)
	}
	return os.MkdirAll(dir, 0o755)
}

func uploadFile(p string, body io.Reader, in fileUploadIn) (*uploadResult, error) {
	mode := fs.FileMode(0o644)
	existed := false
	if st, err := os.Stat(p); err == nil {
		if st.IsDir() {
			return nil, failf(http.StatusConflict, "%s is a directory; name the file to create, or send a .tar.gz with extract=tgz", p)
		}
		if !in.Overwrite {
			return nil, failf(http.StatusConflict, "%s exists; pass overwrite=1 to replace it", p)
		}
		existed, mode = true, st.Mode().Perm()
	}
	if err := ensureDir(filepath.Dir(p), in.Mkdirs); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".mcp-shell-upload-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, failf(http.StatusBadRequest, "receive body: %v (nothing was written)", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if in.SHA256 != "" && sum != in.SHA256 {
		return nil, failf(http.StatusUnprocessableEntity, "sha256 mismatch: received %s, expected %s (nothing was written)", sum, in.SHA256)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, p); err != nil {
		return nil, err
	}
	ok = true
	return &uploadResult{Path: p, Bytes: n, SHA256: sum, Overwrote: existed}, nil
}

// archiveRel turns a tar entry name into a relative path that cannot leave
// the destination; ok is false for absolute names and names with "..".
func archiveRel(name string) (rel string, ok bool) {
	name = strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", false
	}
	c := path.Clean(name)
	if c == "." {
		return "", true
	}
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return filepath.FromSlash(c), true
}

// uploadTree extracts a .tar.gz into dir: first into a staging directory
// inside dir, then, once the archive is complete and matches sha256, each
// file is renamed into place.
func uploadTree(dir string, body io.Reader, in fileUploadIn) (*uploadResult, error) {
	if err := ensureDir(dir, in.Mkdirs); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(dir, ".mcp-shell-upload-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	h := sha256.New()
	src := io.TeeReader(body, h)
	gz, err := gzip.NewReader(src)
	if err != nil {
		return nil, failf(http.StatusBadRequest, "body is not a gzip stream: %v", err)
	}
	tr := tar.NewReader(gz)
	var files, dirs, skipped []string
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, failf(http.StatusBadRequest, "read archive: %v (nothing was written)", err)
		}
		rel, ok := archiveRel(hdr.Name)
		if !ok {
			return nil, failf(http.StatusBadRequest, "unsafe path %q in archive (nothing was written)", hdr.Name)
		}
		if rel == "" {
			continue
		}
		target := filepath.Join(stage, rel)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, err
			}
			dirs = append(dirs, rel)
		case tar.TypeReg, '\x00':
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return nil, err
			}
			mode := hdr.FileInfo().Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode|0o600)
			if err != nil {
				return nil, err
			}
			n, err := io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return nil, failf(http.StatusBadRequest, "extract %s: %v (nothing was written)", rel, err)
			}
			total += n
			files = append(files, rel)
		default:
			skipped = append(skipped, rel)
		}
	}
	// The gzip trailer and tar padding are part of what the client hashed.
	if _, err := io.Copy(io.Discard, src); err != nil {
		return nil, failf(http.StatusBadRequest, "receive body: %v (nothing was written)", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if in.SHA256 != "" && sum != in.SHA256 {
		return nil, failf(http.StatusUnprocessableEntity, "sha256 mismatch: received %s, expected %s (nothing was written)", sum, in.SHA256)
	}
	if !in.Overwrite {
		var exist []string
		for _, rel := range files {
			if _, err := os.Lstat(filepath.Join(dir, rel)); err == nil {
				exist = append(exist, rel)
			}
		}
		if len(exist) > 0 {
			return nil, failf(http.StatusConflict, "%d files already exist in %s (first: %s); pass overwrite=1 to replace them (nothing was written)", len(exist), dir, exist[0])
		}
	}
	for _, rel := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, rel), 0o755); err != nil {
			return nil, err
		}
	}
	for i, rel := range files {
		dst := filepath.Join(dir, rel)
		if st, err := os.Lstat(dst); err == nil && st.IsDir() {
			return nil, fmt.Errorf("%s is a directory, cannot replace it with a file (%d of %d files were moved into place)", dst, i, len(files))
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.Rename(filepath.Join(stage, rel), dst); err != nil {
			return nil, fmt.Errorf("move %s into place: %w (%d of %d files were moved)", rel, err, i, len(files))
		}
	}
	return &uploadResult{Path: dir, Bytes: total, SHA256: sum, Files: len(files), Skipped: skipped}, nil
}

// ---------- download ----------

// errStreamStarted marks a failure after the reply has begun, when no error
// status can be sent any more.
var errStreamStarted = errors.New("stream already started")

func hashFile(f *os.File) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, in fileDownloadIn) (string, error) {
	if strings.TrimSpace(in.Path) == "" {
		return "", failf(http.StatusBadRequest, "path is required")
	}
	p, err := s.guard.Resolve(in.Path)
	if err != nil {
		return "", &httpError{code: http.StatusForbidden, err: err}
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return downloadTree(w, r, p, in.Exclude)
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum, err := hashFile(f)
	if err != nil {
		return "", err
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(st.Size()))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(p)))
	w.Header().Set("X-Content-SHA256", sum)
	if r.Method == http.MethodHead {
		return fmt.Sprintf("described %s (%d bytes, sha256 %s)", p, st.Size(), sum), nil
	}
	n, err := io.Copy(&idleWriter{w: w, rc: http.NewResponseController(w)}, f)
	if err != nil {
		return "", fmt.Errorf("%w: sent %d of %d bytes: %v", errStreamStarted, n, st.Size(), err)
	}
	return fmt.Sprintf("sent %s (%d bytes, sha256 %s)", p, n, sum), nil
}

// downloadTree streams dir as a .tar.gz with regular files and directories;
// the sha256 of the stream follows as the X-Content-SHA256 trailer.
func downloadTree(w http.ResponseWriter, r *http.Request, dir, exclude string) (string, error) {
	skip := map[string]bool{}
	for _, name := range strings.Split(exclude, ",") {
		if name = strings.TrimSpace(name); name != "" {
			skip[name] = true
		}
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(dir)+".tar.gz"))
	w.Header().Set("Trailer", "X-Content-SHA256")
	if r.Method == http.MethodHead {
		return "described " + dir + " (directory, sent as tar.gz)", nil
	}
	h := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(&idleWriter{w: w, rc: http.NewResponseController(w)}, h))
	tw := tar.NewWriter(gz)
	var files, skipped int
	var total int64
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		if skip[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			skipped++
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		hdr.Uname, hdr.Gname = "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		n, err := io.Copy(tw, f)
		f.Close()
		total += n
		files++
		return err
	})
	if err := tw.Close(); walkErr == nil {
		walkErr = err
	}
	if err := gz.Close(); walkErr == nil {
		walkErr = err
	}
	if walkErr != nil {
		return "", fmt.Errorf("%w: archive %s: %v", errStreamStarted, dir, walkErr)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	w.Header().Set("X-Content-SHA256", sum)
	out := fmt.Sprintf("sent %s as tar.gz (%d files, %d bytes before compression, sha256 %s)", dir, files, total, sum)
	if skipped > 0 {
		out += fmt.Sprintf(", skipped %d non-regular entries", skipped)
	}
	return out, nil
}

// ---------- file_info and read_files ----------

type fileInfoIn struct {
	Paths []string `json:"paths" jsonschema:"files or directories on this host (up to 50)"`
}

// fileInfo is the file_info tool: sha256 and size of files, file count and
// total size of directories.
func (s *Server) fileInfo(in fileInfoIn) (string, error) {
	if len(in.Paths) == 0 {
		return "", errors.New("paths is required")
	}
	if len(in.Paths) > fileInfoMaxPaths {
		return "", fmt.Errorf("at most %d paths per call", fileInfoMaxPaths)
	}
	var b strings.Builder
	for _, raw := range in.Paths {
		p, err := s.guard.Resolve(raw)
		if err == nil {
			err = describeFile(&b, p)
		}
		if err != nil {
			fmt.Fprintf(&b, "error  %s: %v\n", raw, err)
		}
	}
	return b.String(), nil
}

func describeFile(b *strings.Builder, p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if st.IsDir() {
		var files int
		var total int64
		err := filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
				files++
				total += info.Size()
			}
			return nil
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(b, "dir  %d files  %d bytes  %s\n", files, total, p)
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	sum, err := hashFile(f)
	if err != nil {
		return err
	}
	fmt.Fprintf(b, "%s  %d bytes  %s  %s\n", sum, st.Size(), st.ModTime().Format("2006-01-02 15:04:05"), p)
	return nil
}

type readFilesIn struct {
	Files []readFileIn `json:"files" jsonschema:"files to read (up to 20), each with optional offset and limit"`
}

// readFiles is the read_files tool: several read_file calls in one, the
// read budget shared between the files.
func (s *Server) readFiles(in readFilesIn) (string, error) {
	if len(in.Files) == 0 {
		return "", errors.New("files is required")
	}
	if len(in.Files) > readFilesMax {
		return "", fmt.Errorf("at most %d files per call", readFilesMax)
	}
	sub := *s.fs
	budget := s.fs.MaxReadBytes
	if budget <= 0 {
		budget = 60_000
	}
	if share := budget / len(in.Files); share < 4_000 {
		sub.MaxReadBytes = 4_000
	} else {
		sub.MaxReadBytes = share
	}
	var b strings.Builder
	for i, f := range in.Files {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "==> %s <==\n", f.Path)
		out, err := sub.ReadFile(f.Path, f.Offset, f.Limit)
		if err != nil {
			fmt.Fprintf(&b, "error: %v\n", err)
			continue
		}
		b.WriteString(out)
	}
	return b.String(), nil
}
