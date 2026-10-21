// canvas-harness prepares immutable suite archives and serves model-free producers.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agent-primitives/canvas/internal/canvas"
	"github.com/agent-primitives/canvas/internal/contract"
	"github.com/agent-primitives/canvas/internal/sessionhost"
	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/harnesswire"
	"google.golang.org/grpc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "canvas-harness:", err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("expected prepare, serve or executor")
	}
	f := flag.NewFlagSet("canvas-harness "+args[0], flag.ContinueOnError)
	f.SetOutput(out)
	switch args[0] {
	case "prepare":
		definition := f.String("definition", "", "Canvas native suite definition")
		output := f.String("out", "", "new private archive directory (must not exist)")
		bind := f.String("bind", "127.0.0.1", "literal 127.0.0.1 or explicit private-pod 0.0.0.0")
		port := f.String("base-port", "9000", "first fixed numeric producer port")
		simulated := f.Bool("simulated-announcement", false, "include the fixed model-free simulated announcement producer")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *definition == "" || *output == "" {
			return errors.New("prepare requires -definition and -out")
		}
		return prepareInventory(*definition, *output, *bind, *port, *simulated)
	case "serve":
		file := f.String("inventory", "", "prepared inventory.json")
		ready := f.String("ready-listen", "127.0.0.1:8081", "literal loopback or explicit private-pod readiness address")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *file == "" {
			return errors.New("serve requires -inventory")
		}
		return serve(ctx, *file, *ready)
	case "executor":
		return runAnnouncementExecutor(ctx, args[1:], out)
	default:
		return errors.New("expected prepare, serve or executor")
	}
}
func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	return err == nil && (host == "127.0.0.1" || host == "0.0.0.0") && validPort(port)
}
func validPort(port string) bool {
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535 && strconv.Itoa(n) == port
}

// Pin every directory component rather than following symlinks, even inside the root.
func childDirectory(parent *os.Root, name string) (*os.Root, error) {
	root, err := parent.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == "." || part == "" {
			continue
		}
		info, e := root.Lstat(part)
		if part == ".." || e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, errors.New("unsafe archive directory")
		}
		next, e := root.OpenRoot(part)
		if e != nil {
			root.Close()
			return nil, errors.New("cannot open archive directory")
		}
		opened, e := next.Stat(".")
		root.Close()
		if e != nil || !os.SameFile(info, opened) {
			next.Close()
			return nil, errors.New("archive directory changed")
		}
		root = next
	}
	return root, nil
}
func directory(name string) (*os.Root, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot("/")
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return childDirectory(root, strings.TrimPrefix(abs, "/"))
}
func readFile(root *os.Root, name string, limit int64) ([]byte, error) {
	parent, err := childDirectory(root, filepath.Dir(name))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	base := filepath.Base(name)
	info, err := parent.Lstat(base)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("expected bounded regular archive file")
	}
	file, err := parent.OpenFile(base, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("cannot open archive file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("archive file changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("cannot read bounded archive file")
	}
	return data, nil
}
func writeNew(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func prepareInventory(definition, output, bind, port string, simulated bool) (err error) {
	if !validAddress(net.JoinHostPort(bind, port)) {
		return errors.New("invalid producer bind or base port")
	}
	abs, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	parent, err := directory(filepath.Dir(abs))
	if err != nil {
		return fmt.Errorf("output parent: %w", err)
	}
	defer parent.Close()
	base := filepath.Base(abs)
	if err = parent.Mkdir(base, 0700); err != nil {
		return errors.New("output directory must be new")
	}
	// Only this newly created directory is removed on failure; existing contracts are never reset.
	defer func() {
		if err != nil {
			parent.RemoveAll(base)
		}
	}()
	root, err := parent.OpenRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	d, err := canvas.LoadGenericDefinitionWithResolver(definition, contract.NewReleaseResolver(filepath.Join(abs, "suite-cache")))
	if err != nil {
		return fmt.Errorf("prepare definition: %w", err)
	}
	n, _ := strconv.Atoi(port)
	count := len(d.Bundles)
	if simulated {
		count++
	}
	if len(d.Bundles) == 0 || count > 32 || n+count-1 > 65535 {
		return errors.New("invalid producer count or port range")
	}
	inv := inventory{Version: 1}
	kind := ""
	if simulated {
		inv.Version, kind = 2, "bundle"
	}
	seen := map[string]bool{}
	for i, b := range d.Bundles {
		parsed, e := contract.Parse(b.Source)
		if e != nil || parsed.Digest != b.Digest || parsed.ID != b.ID {
			return errors.New("invalid compiled archive identity")
		}
		if _, e = sessionhost.NewBundleHarness(parsed, 4); e != nil {
			return fmt.Errorf("prepare native archive %d: %w", i, e)
		}
		if seen[parsed.Digest] {
			return errors.New("duplicate archive identity")
		}
		seen[parsed.Digest] = true
		inv.Entries = append(inv.Entries, entry{Kind: kind, Bundle: "contracts/" + parsed.Digest + "/agent.yaml", Address: net.JoinHostPort(bind, strconv.Itoa(n+i))})
	}
	if err = root.Mkdir("contracts", 0700); err != nil {
		return err
	}
	for i, e := range inv.Entries {
		if err = root.Mkdir(filepath.Dir(e.Bundle), 0700); err != nil {
			return err
		}
		if err = writeNew(root, e.Bundle, d.Bundles[i].Source); err != nil {
			return err
		}
	}
	if simulated {
		inv.Entries = append(inv.Entries, entry{Kind: "simulated-announcement", Address: net.JoinHostPort(bind, strconv.Itoa(n+len(d.Bundles)))})
	}
	data, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		return err
	}
	return writeNew(root, "inventory.json", append(data, '\n'))
}
func serve(ctx context.Context, filename, readyAddress string) error {
	if !validAddress(readyAddress) {
		return errors.New("invalid readiness address")
	}
	abs, err := filepath.Abs(filename)
	if err != nil {
		return err
	}
	root, err := directory(filepath.Dir(abs))
	if err != nil {
		return fmt.Errorf("inventory directory: %w", err)
	}
	defer root.Close()
	data, err := readFile(root, filepath.Base(abs), 65536)
	if err != nil {
		return fmt.Errorf("inventory: %w", err)
	}
	inv, err := decodeInventory(data)
	if err != nil {
		return err
	}
	servers := make([]*grpc.Server, 0, len(inv.Entries))
	listeners := make([]net.Listener, 0, len(inv.Entries)+1)
	defer func() {
		for _, s := range servers {
			s.Stop()
		}
		for _, l := range listeners {
			l.Close()
		}
	}()
	identities, addresses := map[string]bool{}, map[string]bool{}
	// Validate the complete immutable inventory before opening any listener.
	for i, e := range inv.Entries {
		if !validAddress(e.Address) || addresses[e.Address] {
			return fmt.Errorf("inventory entry %d: invalid or duplicate address", i)
		}
		addresses[e.Address] = true
		// Wildcard and loopback listeners on the same port overlap too.
		_, port, _ := net.SplitHostPort(e.Address)
		_, readyPort, _ := net.SplitHostPort(readyAddress)
		if addresses["port:"+port] || port == readyPort {
			return fmt.Errorf("inventory entry %d: overlapping listener", i)
		}
		addresses["port:"+port] = true
		var har api.Harness
		if e.Kind == "simulated-announcement" {
			if identities["announcement"] {
				return fmt.Errorf("inventory entry %d: duplicate announcement identity", i)
			}
			identities["announcement"] = true
			har = sessionhost.NewAnnouncementHarness()
		} else {
			parts := strings.Split(e.Bundle, "/")
			if len(parts) != 3 || parts[0] != "contracts" || len(parts[1]) != 64 || parts[2] != "agent.yaml" {
				return fmt.Errorf("inventory entry %d: invalid archive path", i)
			}
			for _, c := range parts[1] {
				if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
					return fmt.Errorf("inventory entry %d: invalid digest", i)
				}
			}
			raw, eRead := readFile(root, e.Bundle, 16<<20)
			if eRead != nil {
				return fmt.Errorf("inventory entry %d: %w", i, eRead)
			}
			b, eParse := contract.Parse(raw)
			if eParse != nil || b.Digest != parts[1] || identities[b.Digest] || identities["id:"+b.ID] {
				return fmt.Errorf("inventory entry %d: invalid or duplicate identity", i)
			}
			identities[b.Digest] = true
			identities["id:"+b.ID] = true
			var eHarness error
			har, eHarness = sessionhost.NewBundleHarness(b, 4)
			if eHarness != nil {
				return fmt.Errorf("inventory entry %d: %w", i, eHarness)
			}
		}
		server := grpc.NewServer()
		v1.RegisterHarnessServer(server, harnesswire.NewServer(har))
		servers = append(servers, server)
	}
	for i, e := range inv.Entries {
		l, eListen := net.Listen("tcp", e.Address)
		if eListen != nil {
			return fmt.Errorf("producer listener %d: %w", i, eListen)
		}
		listeners = append(listeners, l)
	}
	ready, err := net.Listen("tcp", readyAddress)
	if err != nil {
		return fmt.Errorf("readiness listener: %w", err)
	}
	listeners = append(listeners, ready)
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	health := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	defer health.Close()
	done := make(chan error, len(servers)+1)
	for i, s := range servers {
		go func() { done <- s.Serve(listeners[i]) }()
	}
	go func() { done <- health.Serve(ready) }()
	select {
	case <-ctx.Done():
		return nil
	case err = <-done:
		return fmt.Errorf("producer service stopped: %w", err)
	}
}
