package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestReleaseRemoteCacheRestartAndConcurrentAliases(t *testing.T) {
	ref := releasedNorthwind(t)
	pin := ref.Ref
	files, err := capture(ref.Layout)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("ambient credentials sent")
		}
		key := "blobs/sha256/" + strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v2/team/writer/manifests/"), "sha256:")
		if strings.Contains(r.URL.Path, "/blobs/") {
			key = "blobs/sha256/" + strings.TrimPrefix(strings.Split(r.URL.Path, "/blobs/")[1], "sha256:")
		}
		if strings.Contains(r.URL.Path, "/manifests/") {
			w.Header().Set("Content-Type", releaseManifestType)
		}
		file, ok := files[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(file.Data)
	}))
	defer server.Close()
	ref.Ref = strings.TrimPrefix(server.URL, "https://") + "/team/writer@" + pin
	ref.Layout = ""
	cache := filepath.Join(t.TempDir(), "private", "suite-cache")
	resolver := NewReleaseResolver(cache)
	resolver.client = server.Client()
	b, err := LoadReleasedSuite("first", ref, resolver)
	if err != nil {
		t.Fatal(err)
	}
	count := requests.Load()
	if count < 3 {
		t.Fatalf("OCI graph not fetched: %d", count)
	}
	server.Close()
	reopened, err := LoadReleasedSuite("first", ref, NewReleaseResolver(cache))
	if err != nil || !reflect.DeepEqual(reopened, b) {
		t.Fatal("offline restart differs", err)
	}
	if requests.Load() != count {
		t.Fatal("cached read contacted registry")
	}
	concurrentCache := filepath.Join(t.TempDir(), "private", "suite-cache")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := releasedNorthwind(t)
			if _, err := LoadReleasedSuite(fmt.Sprintf("alias-%d", i), r, NewReleaseResolver(concurrentCache)); err != nil {
				t.Errorf("concurrent valid release refused: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

func TestReleaseRemoteRefusesRedirectTamperAndBounds(t *testing.T) {
	original := releasedNorthwind(t)
	files, err := capture(original.Layout)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"redirect", "wrong-type", "oversize", "blob-tamper", "descriptor-url"} {
		t.Run(mode, func(t *testing.T) {
			var redirected atomic.Int32
			other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer other.Close()
			manifestBytes := files["blobs/sha256/"+strings.TrimPrefix(original.Ref, "sha256:")].Data
			pin := original.Ref
			if mode == "descriptor-url" {
				var manifest releaseManifest
				if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
					t.Fatal(err)
				}
				manifest.Config.URLs = json.RawMessage(`["` + other.URL + `/external-config"]`)
				manifestBytes, err = json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(manifestBytes)
				pin = fmt.Sprintf("sha256:%x", sum)
			}
			var blobRequests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/manifests/") {
					w.Header().Set("Content-Type", releaseManifestType)
					switch mode {
					case "redirect":
						http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
						return
					case "wrong-type":
						w.Header().Set("Content-Type", "text/html")
					case "oversize":
						w.Write(bytes.Repeat([]byte("x"), (1<<20)+1))
						return
					}
					w.Write(manifestBytes)
					return
				}
				blobRequests.Add(1)
				if mode == "blob-tamper" {
					w.Write([]byte("tamper"))
					return
				}
				_, digest, _ := strings.Cut(r.URL.Path, "/blobs/sha256:")
				w.Write(files["blobs/sha256/"+digest].Data)
			}))
			defer server.Close()
			ref := original
			ref.Layout = ""
			ref.Ref = strings.TrimPrefix(server.URL, "https://") + "/team/writer@" + pin
			cache := filepath.Join(t.TempDir(), "private", "suite-cache")
			resolver := NewReleaseResolver(cache)
			resolver.client = server.Client()
			if _, err := LoadReleasedSuite("alias", ref, resolver); err == nil {
				t.Fatal("unsafe OCI transport accepted")
			}
			if redirected.Load() != 0 {
				t.Fatal("redirect or external descriptor URL followed")
			}
			if mode == "descriptor-url" && blobRequests.Load() != 0 {
				t.Fatal("descriptor URL was not refused before blob fetching")
			}
			entries, _ := os.ReadDir(cache)
			if len(entries) != 0 {
				t.Fatal("failed OCI fetch published cache")
			}
		})
	}
}
