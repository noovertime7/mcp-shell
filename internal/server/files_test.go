package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func filesURL(mcpURL string, q url.Values) string {
	return strings.TrimSuffix(mcpURL, "/mcp") + "/files?" + q.Encode()
}

func doFiles(t *testing.T, method, u string, body []byte, token string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, u, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func tarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if strings.HasSuffix(name, "/") {
			if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestFilesRequiresToken(t *testing.T) {
	_, url, root := newTestServer(t)
	resp, _ := doFiles(t, http.MethodGet, filesURL(url, map[string][]string{"path": {root}}), nil, "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
}

func TestFilesUploadAndDownloadFile(t *testing.T) {
	_, url, root := newTestServer(t)
	data := append([]byte("binary\x00\xff中文\n"), bytes.Repeat([]byte{7}, 100_000)...)
	dest := filepath.Join(root, "sub", "a.bin")

	resp, body := doFiles(t, http.MethodPut, filesURL(url, map[string][]string{"path": {dest}}), data, testToken)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "mkdirs=1") {
		t.Fatalf("missing directory: status %d %s", resp.StatusCode, body)
	}
	resp, body = doFiles(t, http.MethodPut, filesURL(url, map[string][]string{"path": {dest}, "mkdirs": {"1"}, "sha256": {sha(data)}}), data, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d %s", resp.StatusCode, body)
	}
	var res uploadResult
	if err := json.Unmarshal(body, &res); err != nil || res.SHA256 != sha(data) || res.Bytes != int64(len(data)) {
		t.Fatalf("reply %s (%v)", body, err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, data) {
		t.Fatal("file content differs")
	}

	resp, body = doFiles(t, http.MethodPut, filesURL(url, map[string][]string{"path": {dest}}), []byte("x"), testToken)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "overwrite=1") {
		t.Fatalf("existing file without overwrite: %d %s", resp.StatusCode, body)
	}
	resp, body = doFiles(t, http.MethodPut, filesURL(url, map[string][]string{"path": {dest}, "overwrite": {"1"}, "sha256": {sha([]byte("other"))}}), []byte("x"), testToken)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("sha mismatch: %d %s", resp.StatusCode, body)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, data) {
		t.Fatal("a rejected upload must leave the file alone")
	}
	if matches, _ := filepath.Glob(filepath.Join(root, "sub", ".mcp-shell-upload-*")); len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}

	resp, body = doFiles(t, http.MethodGet, filesURL(url, map[string][]string{"path": {dest}}), nil, testToken)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, data) || resp.Header.Get("X-Content-SHA256") != sha(data) {
		t.Fatalf("download: %d, %d bytes, sha header %q", resp.StatusCode, len(body), resp.Header.Get("X-Content-SHA256"))
	}
	resp, _ = doFiles(t, http.MethodGet, filesURL(url, map[string][]string{"path": {filepath.Join(root, "nope")}}), nil, testToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing file: %d", resp.StatusCode)
	}
}

func TestFilesUploadTreeAndDownloadTree(t *testing.T) {
	_, url, root := newTestServer(t)
	dest := filepath.Join(root, "tree")
	archive := tarGz(t, map[string]string{"a.txt": "A", "d/": "", "d/b.txt": "BB", "empty/": ""})
	resp, body := doFiles(t, http.MethodPut, filesURL(url, map[string][]string{"path": {dest}, "extract": {"tgz"}, "mkdirs": {"1"}, "sha256": {sha(archive)}}), archive, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload tree: %d %s", resp.StatusCode, body)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "d", "b.txt")); string(b) != "BB" {
		t.Fatalf("d/b.txt = %q", b)
	}
	if st, err := os.Stat(filepath.Join(dest, "empty")); err != nil || !st.IsDir() {
		t.Fatal("empty directory not created")
	}
	resp, body = doFiles(t, http.MethodPut, filesURL(url, map[string][]string{"path": {dest}, "extract": {"tgz"}}), archive, testToken)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflict without overwrite: %d %s", resp.StatusCode, body)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 3 {
		t.Fatalf("staging directory left behind or files changed: %v", entries)
	}

	evil := tarGz(t, map[string]string{"../escape.txt": "x"})
	resp, _ = doFiles(t, http.MethodPut, filesURL(url, map[string][]string{"path": {dest}, "extract": {"tgz"}, "overwrite": {"1"}}), evil, testToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("path traversal: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); err == nil {
		t.Fatal("archive escaped the destination")
	}

	os.MkdirAll(filepath.Join(dest, ".git"), 0o755)
	os.WriteFile(filepath.Join(dest, ".git", "HEAD"), []byte("ref"), 0o644)
	resp, body = doFiles(t, http.MethodGet, filesURL(url, map[string][]string{"path": {dest}, "exclude": {".git"}}), nil, testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download tree: %d", resp.StatusCode)
	}
	if got := resp.Trailer.Get("X-Content-SHA256"); got != sha(body) {
		t.Fatalf("trailer sha %q, body sha %s", got, sha(body))
	}
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		got[hdr.Name] = string(b)
	}
	if got["a.txt"] != "A" || got["d/b.txt"] != "BB" {
		t.Fatalf("archive = %v", got)
	}
	if _, ok := got["empty/"]; !ok {
		t.Fatalf("empty directory missing from archive: %v", got)
	}
	for name := range got {
		if strings.HasPrefix(name, ".git") {
			t.Fatalf("excluded entry %s in archive", name)
		}
	}
}

func TestFileInfoAndReadFiles(t *testing.T) {
	_, url, root := newTestServer(t)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\n"), 0o644)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("three\n"), 0o644)

	out, isErr := call(t, url, "file_info", map[string]any{"paths": []string{"a.txt", root, "missing.txt"}})
	if isErr || !strings.Contains(out, sha([]byte("one\ntwo\n"))+"  8 bytes") || !strings.Contains(out, "dir  2 files  14 bytes") || !strings.Contains(out, "error  missing.txt") {
		t.Fatalf("file_info:\n%s", out)
	}

	out, isErr = call(t, url, "read_files", map[string]any{"files": []map[string]any{{"path": "a.txt", "offset": 2}, {"path": "b.txt"}, {"path": "missing.txt"}}})
	if isErr || !strings.Contains(out, "==> a.txt <==\n     2\ttwo") || !strings.Contains(out, "==> b.txt <==\n     1\tthree") || !strings.Contains(out, "==> missing.txt <==\nerror:") {
		t.Fatalf("read_files:\n%s", out)
	}

	out, isErr = call(t, url, "write_file", map[string]any{"path": "c.txt", "content": "hi"})
	if isErr || !strings.Contains(out, "sha256 "+sha([]byte("hi"))) {
		t.Fatalf("write_file: %s", out)
	}
}

func TestFilesUploadThatStallsIsDroppedWithoutLeftovers(t *testing.T) {
	old := transferIdle
	transferIdle = 300 * time.Millisecond
	t.Cleanup(func() { transferIdle = old })
	_, url, root := newTestServer(t)

	pr, pw := io.Pipe()
	defer pw.Close()
	req, _ := http.NewRequest(http.MethodPut, filesURL(url, map[string][]string{"path": {filepath.Join(root, "stall.bin")}}), pr)
	req.ContentLength = 1 << 20
	req.Header.Set("Authorization", "Bearer "+testToken)
	done := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				err = io.ErrUnexpectedEOF
			}
		}
		done <- err
	}()
	pw.Write(make([]byte, 1000)) // then stall
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a stalled upload was not dropped")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		left, _ := filepath.Glob(filepath.Join(root, ".mcp-shell-upload-*"))
		if len(left) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("temporary file left behind: %v", left)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(root, "stall.bin")); err == nil {
		t.Fatal("a stalled upload must not create the file")
	}
}

func TestArchiveRel(t *testing.T) {
	cases := map[string]bool{
		"a/b.txt": true, "./a": true, "a/../b": true, ".": true,
		"../a": false, "/etc/passwd": false, "a/../../b": false, `..\a`: false, "C:/x": false,
	}
	for name, want := range cases {
		if _, ok := archiveRel(name); ok != want {
			t.Errorf("archiveRel(%q) ok=%v, want %v", name, ok, want)
		}
	}
}
