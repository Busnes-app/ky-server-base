// Package e2e drives docker-compose.replicas.yml with the locally built image: replicas are
// killed, stopped and cut off from PostgreSQL while a client keeps talking to the proxy.
// It runs only with KY_E2E_COMPOSE=1 (make test-replicas) and removes its own project.
package e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/capsule"
	"github.com/Busnes-app/ky-primitives/recoverykey"
	"github.com/Busnes-app/ky_server_base/internal/auth"
)

type project struct {
	t        *testing.T
	dir      string
	name     string
	env      []string
	url      string
	image    string
	vars     map[string]string
	adminPwd string
}

func secret(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// run runs a command in the project directory with the project's environment.
func (p *project) run(stdin io.Reader, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = p.dir
	cmd.Env = p.env
	cmd.Stdin = stdin
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func (p *project) must(stdin io.Reader, name string, args ...string) string {
	p.t.Helper()
	out, err := p.run(stdin, name, args...)
	if err != nil {
		p.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(out)
}

func (p *project) compose(args ...string) string {
	p.t.Helper()
	return p.must(nil, "docker", append([]string{"compose"}, args...)...)
}

// setVar changes one Compose variable for every later command (the last value wins).
func (p *project) setVar(k, v string) {
	p.vars[k] = v
	p.env = append(p.env, k+"="+v)
}

func startProject(t *testing.T) *project {
	t.Helper()
	image := os.Getenv("KY_IMAGE")
	if image == "" {
		image = "ky_server_base:local"
	}
	dir := t.TempDir()
	for _, f := range []string{"docker-compose.yml", "docker-compose.replicas.yml", "deploy/replicas/haproxy.cfg"} {
		raw, err := os.ReadFile(filepath.Join("..", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// The haproxy user in its image must read the config.
	if err := os.Chmod(filepath.Join(dir, "deploy/replicas/haproxy.cfg"), 0644); err != nil {
		t.Fatal(err)
	}
	octet := 200 + int(secret(1)[0]%50)
	subnet := fmt.Sprintf("172.31.%d.0/24", octet)
	port := freePort(t)
	pgPass := secret(16)
	p := &project{t: t, dir: dir, name: "kye2e" + secret(4), image: image, adminPwd: secret(12)}
	p.url = fmt.Sprintf("http://127.0.0.1:%d", port)
	p.vars = map[string]string{
		"COMPOSE_FILE":         "docker-compose.yml:docker-compose.replicas.yml",
		"KY_IMAGE":             image,
		"KY_REPLICAS":          "2",
		"KY_APP_NAME":          "replica-e2e",
		"KY_APP_URL":           p.url,
		"KY_PROXY_PORT":        fmt.Sprint(port),
		"KY_PROXY_IP":          fmt.Sprintf("172.31.%d.2", octet),
		"KY_REPLICA_SUBNET":    subnet,
		"KY_POSTGRES_PASSWORD": pgPass,
		"KY_DB_DSN":            "postgres://ky_server:" + pgPass + "@postgres:5432/ky_server?sslmode=disable",
		"KY_SESSION_SECRET":    secret(32),
		"KY_SCIM_TOKEN":        secret(24),
		"KY_ENCRYPTION_KEY":    secret(32),
		"KY_BACKUP_KEEP":       "3",
	}
	p.env = append(os.Environ(), "COMPOSE_PROJECT_NAME="+p.name, "PUSH_RELAY_URL=", "APNS_RELAY_URL=")
	for k, v := range p.vars {
		p.env = append(p.env, k+"="+v)
	}
	// Read into every app container through the base file's env_file. Throwaway values only;
	// the office-network exemption keeps this one test client under the per-address limit.
	dotenv := strings.Join([]string{
		"KY_ADMIN_PASSWORD=" + p.adminPwd,
		"KY_CAPTCHA_PROVIDER=none",
		"KY_RATE_LIMIT_EXEMPT_NETWORKS=" + subnet,
		"PUSH_RELAY_URL=",
		"APNS_RELAY_URL=",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(dotenv), 0600); err != nil {
		t.Fatal(err)
	}
	// The shared capsule archive, as an operator's checkout has it (Docker would make it root's).
	if err := os.Mkdir(filepath.Join(dir, "backups"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := p.run(nil, "docker", "compose", "logs", "--no-color", "--tail", "200")
			t.Logf("compose logs:\n%s", out)
		}
		if out, err := p.run(nil, "docker", "compose", "down", "-v", "--remove-orphans", "-t", "30"); err != nil {
			t.Logf("compose down: %v\n%s", err, out)
		}
	})
	// A fresh database must be ready for the runbook's immediate createdb command,
	// not merely healthy while the image's temporary socket-only init server runs.
	p.compose("up", "-d", "--wait", "--wait-timeout", "180", "postgres")
	p.compose("exec", "-T", "postgres", "createdb", "-U", "ky_server", "replica_readiness_probe")
	p.compose("exec", "-T", "postgres", "dropdb", "-U", "ky_server", "replica_readiness_probe")
	p.compose("up", "-d", "--wait", "--wait-timeout", "180")
	return p
}

// appContainers returns the running app replica container IDs.
func (p *project) appContainers() []string {
	p.t.Helper()
	return strings.Fields(p.compose("ps", "-q", "app"))
}

// databaseIP is the container's address on the database network, as PostgreSQL sees it.
func (p *project) databaseIP(id string) string {
	p.t.Helper()
	return p.must(nil, "docker", "inspect", "-f", `{{(index .NetworkSettings.Networks "`+p.name+`_database").IPAddress}}`, id)
}

func (p *project) restartCount(id string) string {
	p.t.Helper()
	return p.must(nil, "docker", "inspect", "-f", "{{.RestartCount}}", id)
}

// restoreReplicas brings the project back to every replica running and ready.
func (p *project) restoreReplicas() {
	p.t.Helper()
	p.compose("up", "-d", "--wait", "--wait-timeout", "180")
	p.waitFor("the proxy sees a ready replica", 60*time.Second, func() bool {
		code, _ := p.get("/readyz", nil)
		return code == http.StatusOK
	})
}

func (p *project) waitFor(what string, limit time.Duration, ok func() bool) time.Duration {
	p.t.Helper()
	start := time.Now()
	for time.Since(start) < limit {
		if ok() {
			return time.Since(start)
		}
		time.Sleep(250 * time.Millisecond)
	}
	p.t.Fatalf("%s: not within %s", what, limit)
	return 0
}

var client = &http.Client{Timeout: 15 * time.Second}

type session []*http.Cookie

func (p *project) do(method, path string, s session, body any) (int, []byte, http.Header) {
	p.t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, p.url+path, r)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range s {
		req.AddCookie(c)
		if c.Name == auth.CSRFCookieName {
			req.Header.Set(auth.HeaderCSRF, c.Value)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, []byte(err.Error()), nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, resp.Header
}

func (p *project) get(path string, s session) (int, []byte) {
	code, body, _ := p.do("GET", path, s, nil)
	return code, body
}

func (p *project) login(user, pass string) (int, session) {
	p.t.Helper()
	code, _, h := p.do("POST", "/api/auth/login", nil, map[string]string{"username": user, "password": pass})
	if code != http.StatusOK {
		return code, nil
	}
	resp := http.Response{Header: h}
	return code, resp.Cookies()
}

func (p *project) mustLogin(user, pass string) session {
	p.t.Helper()
	code, s := p.login(user, pass)
	if code != http.StatusOK {
		p.t.Fatalf("login %s: %d", user, code)
	}
	return s
}

// meLoop asks /api/auth/me at 10 rps until stop closes and returns every failure seen.
func (p *project) meLoop(s session, stop <-chan struct{}) <-chan []string {
	out := make(chan []string, 1)
	go func() {
		var failures []string
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		var wg sync.WaitGroup
		var mu sync.Mutex
		for {
			select {
			case <-stop:
				wg.Wait()
				out <- failures
				return
			case <-tick.C:
				wg.Add(1)
				go func() {
					defer wg.Done()
					code, body := p.get("/api/auth/me", s)
					if code/100 != 2 || !bytes.Contains(body, []byte(`"authenticated":true`)) {
						mu.Lock()
						failures = append(failures, fmt.Sprintf("%s %d %s", time.Now().Format("15:04:05.000"), code, body))
						mu.Unlock()
					}
				}()
			}
		}
	}()
	return out
}

// psql runs one query on the project database and returns its unaligned output.
func (p *project) psql(query string) string {
	p.t.Helper()
	return p.compose("exec", "-T", "postgres", "psql", "-U", "ky_server", "-d", "ky_server", "-Atc", query)
}

// workerIP is the database address of the replica holding the scheduled-worker lock, or "".
func (p *project) workerIP() string {
	p.t.Helper()
	out, err := p.run(nil, "docker", "compose", "exec", "-T", "postgres", "psql", "-U", "ky_server", "-d", "ky_server", "-Atc",
		`SELECT host(a.client_addr) FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
WHERE l.locktype = 'advisory' AND l.granted AND l.classid = 3
AND l.objid::bigint = (hashtext('public:scheduled-worker')::bigint + 4294967296) % 4294967296`)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (p *project) containerWithIP(ip string) string {
	p.t.Helper()
	for _, id := range p.appContainers() {
		if p.databaseIP(id) == ip {
			return id
		}
	}
	return ""
}

// probe asks one replica from inside its own container (replicas publish no port).
func (p *project) probe(id, path string) int {
	out, err := p.run(nil, "docker", "exec", id, "wget", "-q", "-S", "-T", "2", "-O", "/dev/null", "http://127.0.0.1:8080"+path)
	for _, code := range []int{200, 503} {
		if strings.Contains(out, fmt.Sprintf("HTTP/1.1 %d", code)) {
			return code
		}
	}
	if err == nil {
		return 200
	}
	return 0
}

// deposit seals a capsule through the proxy and copies it out of whichever replica wrote it.
func (p *project) deposit(s session) []byte {
	p.t.Helper()
	code, body, _ := p.do("POST", "/api/backup/deposit", s, nil)
	if code != http.StatusOK {
		p.t.Fatalf("deposit through the proxy: %d %s", code, body)
	}
	var res struct {
		LocalPath string `json:"local_path"`
	}
	if err := json.Unmarshal(body, &res); err != nil || res.LocalPath == "" {
		p.t.Fatalf("deposit result %s: %v", body, err)
	}
	dest := filepath.Join(p.t.TempDir(), "capsule.kycap")
	for _, id := range p.appContainers() {
		if _, err := p.run(nil, "docker", "cp", id+":"+res.LocalPath, dest); err == nil {
			raw, err := os.ReadFile(dest)
			if err != nil {
				p.t.Fatal(err)
			}
			manifest, err := capsule.ReadUnverifiedManifest(raw)
			if err != nil {
				p.t.Fatal(err)
			}
			if manifest.ServiceName != p.vars["KY_APP_NAME"] {
				p.t.Fatalf("capsule service %q differs from configured %q", manifest.ServiceName, p.vars["KY_APP_NAME"])
			}
			return raw
		}
	}
	p.t.Fatalf("no replica holds %s", res.LocalPath)
	return nil
}

// burst sends authenticated requests from the moment the database froze: four at once, then
// one every half second for five seconds, with a client timeout above the bound and no retry.
// The returned func waits for them. Every one must be answered 502/503/504 within 10 s (the
// proxy's 8 s server timeout ends the ones a replica holds), and a 502/503 must arrive by
// then: a request is never left hanging on the frozen database.
func (p *project) burst(s session) func() {
	type answer struct {
		path       string
		code       int
		sent, took time.Duration
		err        error
	}
	start := time.Now()
	answers := make(chan answer, 16)
	c := &http.Client{Timeout: 12 * time.Second}
	send := func(path string) {
		sent := time.Now()
		req, _ := http.NewRequest("GET", p.url+path, nil)
		for _, ck := range s {
			req.AddCookie(ck)
		}
		resp, err := c.Do(req)
		a := answer{path: path, sent: sent.Sub(start), took: time.Since(sent), err: err}
		if err == nil {
			a.code = resp.StatusCode
			resp.Body.Close()
		}
		answers <- a
	}
	n := 0
	paths := []string{"/api/auth/me", "/api/backup/status"}
	for i := range 4 {
		n++
		go send(paths[i%len(paths)])
	}
	go func() {
		for i := range 10 {
			time.Sleep(500 * time.Millisecond)
			go send(paths[i%len(paths)])
		}
	}()
	n += 10
	return func() {
		p.t.Helper()
		var quick bool
		for range n {
			a := <-answers
			switch {
			case a.err != nil:
				p.t.Errorf("request sent %s after the freeze: no answer in %s: %v", a.sent.Round(time.Millisecond), a.took.Round(time.Millisecond), a.err)
			case a.code != http.StatusBadGateway && a.code != http.StatusServiceUnavailable && a.code != http.StatusGatewayTimeout:
				p.t.Errorf("request sent %s after the freeze: %d, want 502/503/504", a.sent.Round(time.Millisecond), a.code)
			case a.took > 10*time.Second:
				p.t.Errorf("request sent %s after the freeze took %s", a.sent.Round(time.Millisecond), a.took.Round(time.Millisecond))
			case a.code != http.StatusGatewayTimeout && a.sent+a.took <= 10*time.Second:
				quick = true
			}
			p.t.Logf("frozen database: %s sent +%s, %d after %s", a.path, a.sent.Round(100*time.Millisecond), a.code, a.took.Round(100*time.Millisecond))
		}
		if !quick {
			p.t.Error("no request got a 502/503 within 10 s of the freeze")
		}
	}
}

// localCopies is the capsule list the Backup page shows, as one replica answered it.
func (p *project) localCopies(s session) []string {
	p.t.Helper()
	code, body := p.get("/api/backup/status", s)
	var status struct {
		LocalCopies []struct {
			Name string `json:"name"`
		} `json:"local_copies"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &status) != nil {
		p.t.Fatalf("backup status %d %s", code, body)
	}
	var names []string
	for _, c := range status.LocalCopies {
		names = append(names, c.Name)
	}
	return names
}

// sameCopiesEverywhere asks the proxy enough times to reach every replica and requires one list.
func (p *project) sameCopiesEverywhere(s session, want int) []string {
	p.t.Helper()
	first := p.localCopies(s)
	if len(first) != want {
		p.t.Fatalf("%d local copies listed, want %d: %v", len(first), want, first)
	}
	for range 3 * len(p.appContainers()) {
		if got := p.localCopies(s); strings.Join(got, ",") != strings.Join(first, ",") {
			p.t.Fatalf("replicas list different local copies: %v and %v", first, got)
		}
	}
	return first
}

func TestReplicaFailover(t *testing.T) {
	if os.Getenv("KY_E2E_COMPOSE") != "1" {
		t.Skip("set KY_E2E_COMPOSE=1 (make test-replicas) to run the Compose replica tests")
	}
	p := startProject(t)
	if n := len(p.appContainers()); n != 2 {
		t.Fatalf("%d app replicas, want 2", n)
	}

	// The bootstrap admin replaces its password first; every later step signs in with it.
	first := p.mustLogin("admin", p.adminPwd)
	newPwd := "replica-" + secret(12)
	if code, body, _ := p.do("POST", "/api/auth/change-password", first, map[string]string{"current_password": p.adminPwd, "new_password": newPwd}); code != http.StatusOK {
		t.Fatalf("change password: %d %s", code, body)
	}
	admin := p.mustLogin("admin", newPwd)

	// A throwaway 2-of-3 kit, pinned by hand on whichever replica the proxy picks.
	priv, err := recoverykey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	split, err := recoverykey.Split(priv, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	shares := split[0].String() + "\n" + split[2].String() + "\n"
	// Subtests run in order; the project's helpers report to the one running.
	step := func(name string, f func(t *testing.T)) {
		t.Run(name, func(t *testing.T) {
			parent := p.t
			p.t = t
			defer func() { p.t = parent }()
			f(t)
		})
	}
	pin := map[string]any{"public_key": encodeKey(priv), "threshold": 2, "total_shares": 3}
	if code, body, _ := p.do("POST", "/api/backup/pin-key", admin, pin); code != http.StatusOK {
		t.Fatalf("pin: %d %s", code, body)
	}

	step("AC17 SIGKILL one replica under load", func(t *testing.T) {
		stop := make(chan struct{})
		results := p.meLoop(admin, stop)
		time.Sleep(2 * time.Second)
		victim := p.appContainers()[0]
		p.must(nil, "docker", "kill", "-s", "KILL", victim)
		if code, _ := p.login("admin", newPwd); code != http.StatusOK {
			t.Errorf("login straight after the kill: %d", code)
		}
		time.Sleep(6 * time.Second)
		close(stop)
		if failures := <-results; len(failures) > 0 {
			t.Errorf("%d non-2xx answers while a replica was killed:\n%s", len(failures), strings.Join(failures, "\n"))
		}
		p.restoreReplicas()
	})

	step("AC18 graceful stop of one replica under load", func(t *testing.T) {
		victim := p.appContainers()[0]
		stop := make(chan struct{})
		results := p.meLoop(admin, stop)
		time.Sleep(2 * time.Second)
		stopped := make(chan string, 1)
		go func() { out, _ := p.run(nil, "docker", "stop", "-t", "30", victim); stopped <- out }()
		var unreadyAt, liveAt time.Time
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) && unreadyAt.IsZero() {
			if p.probe(victim, "/readyz") == http.StatusServiceUnavailable {
				unreadyAt = time.Now()
				if p.probe(victim, "/livez") == http.StatusOK {
					liveAt = time.Now()
				}
			}
		}
		<-stopped
		close(stop)
		if failures := <-results; len(failures) > 0 {
			t.Errorf("%d non-2xx answers during a graceful stop:\n%s", len(failures), strings.Join(failures, "\n"))
		}
		if unreadyAt.IsZero() || liveAt.IsZero() {
			t.Error("the stopping replica never answered /readyz 503 while still serving /livez")
		}
		logs := p.must(nil, "docker", "logs", victim)
		drain, shut := strings.Index(logs, "[KY-BASE] draining"), strings.Index(logs, "Shutting down gracefully")
		if drain < 0 || shut < drain {
			t.Errorf("logs do not show the drain before the listener closed:\n%s", logs)
		}
		p.restoreReplicas()
	})

	step("AC21 security state is shared", func(t *testing.T) {
		app := p.appContainers()[0]
		lockPwd := "lockme-" + secret(8)
		p.must(nil, "docker", "exec", app, "/app/ky_server_base", "init-admin", "-username", "lockme", "-password", lockPwd)
		for i := range 10 {
			if code, _ := p.login("lockme", "wrong-"+fmt.Sprint(i)); code != http.StatusUnauthorized {
				t.Fatalf("failed login %d: %d", i, code)
			}
		}
		if code, _ := p.login("lockme", lockPwd); code != http.StatusUnauthorized {
			t.Fatalf("the right password after 10 failures across replicas: %d, want 401 (locked)", code)
		}
		// Whichever replica recorded the last failure, every one dies here.
		for _, id := range p.appContainers() {
			p.must(nil, "docker", "kill", "-s", "KILL", id)
		}
		p.restoreReplicas()
		if code, _ := p.login("lockme", lockPwd); code != http.StatusUnauthorized {
			t.Fatalf("the lockout did not survive the replicas: %d", code)
		}

		s := p.mustLogin("admin", newPwd)
		for i := range 20 {
			if code, _ := p.get("/api/admin/users", s); code != http.StatusOK {
				t.Fatalf("request %d before logout: %d", i, code)
			}
		}
		if code, _, _ := p.do("POST", "/api/auth/logout", s, nil); code != http.StatusOK {
			t.Fatalf("logout: %d", code)
		}
		for i := range 20 {
			if code, _ := p.get("/api/admin/users", s); code != http.StatusUnauthorized {
				t.Fatalf("request %d after logout: %d, want 401", i, code)
			}
		}
	})

	step("AC19 elected worker dies", func(t *testing.T) {
		var ip string
		p.waitFor("a replica holds scheduled-worker", 75*time.Second, func() bool { ip = p.workerIP(); return ip != "" })
		victim := p.containerWithIP(ip)
		if victim == "" {
			t.Fatalf("no replica has database address %s", ip)
		}
		p.must(nil, "docker", "kill", "-s", "KILL", victim)
		took := p.waitFor("another replica holds scheduled-worker", 75*time.Second, func() bool {
			now := p.workerIP()
			return now != "" && now != ip
		})
		t.Logf("worker re-elected %s after the kill", took.Round(time.Second))
		raw := p.deposit(admin)
		if _, files, err := capsule.Open(raw, priv, ""); err != nil || len(files) < 3 {
			t.Fatalf("capsule after failover: %v", err)
		}
		p.restoreReplicas()
	})

	step("local capsules are one archive", func(t *testing.T) {
		// AC19 left one capsule; four more through round robin keep the newest three.
		for range 4 {
			p.deposit(admin)
		}
		files, _ := filepath.Glob(filepath.Join(p.dir, "backups", "*.kycap"))
		if len(files) != 3 {
			t.Fatalf("%d capsules in ./backups, want KY_BACKUP_KEEP=3: %v", len(files), files)
		}
		kept := p.sameCopiesEverywhere(admin, 3)

		// A plain down and up gives every replica a new data volume; the archive stays.
		p.compose("down", "-t", "30")
		p.restoreReplicas()
		if got := p.sameCopiesEverywhere(admin, 3); strings.Join(got, ",") != strings.Join(kept, ",") {
			t.Fatalf("down/up changed the archive: %v, was %v", got, kept)
		}

		// Scaling up adds a replica and an HAProxy slot; it lists and prunes the same archive.
		p.setVar("KY_REPLICAS", "3")
		p.restoreReplicas()
		if n := len(p.appContainers()); n != 3 {
			t.Fatalf("%d replicas after scaling to 3", n)
		}
		p.sameCopiesEverywhere(admin, 3)
		p.deposit(admin)
		p.sameCopiesEverywhere(admin, 3)
		p.setVar("KY_REPLICAS", "2")
		p.restoreReplicas()
		if n := len(p.appContainers()); n != 2 {
			t.Fatalf("%d replicas after scaling back to 2", n)
		}
		p.sameCopiesEverywhere(admin, 3)
	})

	step("CLI drill and export on PostgreSQL", func(t *testing.T) {
		app := p.appContainers()[0]
		if out := p.must(nil, "docker", "exec", app, "/app/ky_server_base", "backup-drill"); !strings.Contains(out, "PASSED") {
			t.Fatalf("backup-drill:\n%s", out)
		}
		p.must(nil, "docker", "exec", app, "/app/ky_server_base", "export-capsule", "-out", "/tmp/e2e.kycap")
		p.must(nil, "docker", "exec", app, "rm", "/tmp/e2e.kycap")
	})

	step("AC20 database paused, then stopped", func(t *testing.T) {
		ids := p.appContainers()
		before := map[string]string{}
		for _, id := range ids {
			before[id] = p.restartCount(id)
		}
		var workerID string
		p.waitFor("a worker is elected", 75*time.Second, func() bool { workerID = p.containerWithIP(p.workerIP()); return workerID != "" })
		disrupted := func(how string) {
			start := time.Now()
			for _, id := range ids {
				p.waitFor(how+": /readyz 503 on "+id[:12], 5*time.Second-time.Since(start), func() bool { return p.probe(id, "/readyz") == http.StatusServiceUnavailable })
				if code := p.probe(id, "/livez"); code != http.StatusOK {
					t.Errorf("%s: /livez %d on %s", how, code, id[:12])
				}
			}
			// New requests get a 502/503 once the checks have taken every replica out. While
			// paused, burst proves it without retries: a polling request that reached a replica
			// still in rotation would spend the budget on its own 8 s timeout. Stopped, nothing
			// waits, so each attempt answers at once.
			if how == "stopped" {
				p.waitFor(how+": the proxy answers 502/503", 10*time.Second-time.Since(start), func() bool {
					code, _ := p.get("/api/auth/me", admin)
					return code == http.StatusBadGateway || code == http.StatusServiceUnavailable
				})
			}
			t.Logf("%s: every replica unready %s after the disruption", how, time.Since(start).Round(100*time.Millisecond))
		}
		defer func() { // leave a database behind for the next step whatever happened here
			_, _ = p.run(nil, "docker", "compose", "unpause", "postgres")
			_, _ = p.run(nil, "docker", "compose", "start", "postgres")
		}()
		p.compose("pause", "postgres")
		inFlight := p.burst(admin)
		disrupted("paused")
		inFlight()
		p.compose("unpause", "postgres")
		p.compose("stop", "postgres")
		disrupted("stopped")
		p.waitFor("the worker gives up ownership", 15*time.Second, func() bool {
			logs, _ := p.run(nil, "docker", "logs", "--since", "60s", workerID)
			return strings.Contains(logs, "[WORKER] ownership lost or unavailable")
		})
		p.compose("start", "postgres")
		start := time.Now()
		for _, id := range ids {
			p.waitFor("/readyz 200 again on "+id[:12], 15*time.Second-time.Since(start)+time.Second, func() bool { return p.probe(id, "/readyz") == http.StatusOK })
		}
		p.waitFor("the earlier session authorizes", 15*time.Second, func() bool {
			code, _ := p.get("/api/admin/users", admin)
			return code == http.StatusOK
		})
		p.waitFor("a worker is re-elected", 75*time.Second, func() bool { return p.workerIP() != "" })
		for _, id := range ids {
			if after := p.restartCount(id); after != before[id] {
				t.Errorf("replica %s restarted (%s -> %s)", id[:12], before[id], after)
			}
		}
	})

	step("AC22 real-binary PostgreSQL restore drill", func(t *testing.T) {
		raw := p.deposit(admin)
		restored := p.name + "-restored"
		t.Cleanup(func() { _, _ = p.run(nil, "docker", "rm", "-f", restored) })
		relayOff := []string{"-e", "PUSH_RELAY_URL=", "-e", "APNS_RELAY_URL="}
		in := filepath.Join(p.dir, "in.kycap")
		if err := os.WriteFile(in, raw, 0600); err != nil {
			t.Fatal(err)
		}
		// docs/RESTORE.md as written: the restore and migrate-postgres commands below are the
		// runbook's, run from the project directory with the shares on stdin.
		out := filepath.Join(p.dir, "restored")
		if err := os.Mkdir(out, 0700); err != nil {
			t.Fatal(err)
		}
		user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
		said := p.must(strings.NewReader(shares), "docker", "compose", "run", "--rm", "--no-deps", "--user", user,
			"-v", in+":/in.kycap:ro", "-v", out+":/restored", "app", "restore", "-capsule", "/in.kycap", "-to", "/restored")
		for _, want := range []string{"Scheduled deposits are OFF", "Sessions deleted", "Replica configuration record removed"} {
			if !strings.Contains(said, want) {
				t.Fatalf("restore output lacks %q:\n%s", want, said)
			}
		}
		p.compose("exec", "-T", "postgres", "createdb", "-U", "ky_server", "ky_server_restored")
		target := "postgres://ky_server:" + p.vars["KY_POSTGRES_PASSWORD"] + "@postgres:5432/ky_server_restored?sslmode=disable"
		if err := os.WriteFile(filepath.Join(p.dir, "migration.env"), []byte("KY_MIGRATE_POSTGRES_DSN="+target+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, check := range [][]string{{"--check"}, {}} {
			p.must(nil, "docker", append([]string{"compose", "run", "--rm", "--no-deps", "--user", user, "--env-from-file", "migration.env",
				"-v", out + ":/restored", "app", "migrate-postgres", "--source", "/restored/data/ky_server.db"}, check...)...)
		}
		key, err := os.ReadFile(filepath.Join(out, "data", "encryption.key"))
		if err != nil {
			t.Fatal(err)
		}

		create := []string{"create", "--name", restored, "--network", p.name + "_replicas", "-p", "127.0.0.1::8080",
			"-e", "KY_DB_DRIVER=postgres", "-e", "KY_DB_DSN=" + target, "-e", "KY_ENCRYPTION_KEY=" + strings.TrimSpace(string(key)),
			"-e", "KY_SESSION_SECRET=" + p.vars["KY_SESSION_SECRET"], "-e", "KY_SCIM_TOKEN=" + p.vars["KY_SCIM_TOKEN"],
			"-e", "KY_CAPTCHA_PROVIDER=none", "-e", "KY_APP_NAME=" + p.vars["KY_APP_NAME"]}
		p.must(nil, "docker", append(append(create, relayOff...), p.image)...)
		p.must(nil, "docker", "network", "connect", p.name+"_database", restored)
		p.must(nil, "docker", "start", restored)
		addr := p.must(nil, "docker", "port", restored, "8080/tcp")
		other := &project{t: t, url: "http://" + strings.Fields(addr)[0]}
		other.waitFor("the restored server is ready", 60*time.Second, func() bool {
			code, _ := other.get("/readyz", nil)
			return code == http.StatusOK
		})
		s := other.mustLogin("admin", newPwd)
		code, body := other.get("/api/backup/status", s)
		var status struct {
			IntervalSec int64 `json:"interval_sec"`
			KeyPinned   bool  `json:"key_pinned"`
		}
		if code != http.StatusOK || json.Unmarshal(body, &status) != nil || status.IntervalSec != 0 || !status.KeyPinned {
			t.Fatalf("restored status %d %s: want the schedule off and the key pinned", code, body)
		}
	})
}

func encodeKey(priv recoverykey.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(priv.Public().Bytes())
}
