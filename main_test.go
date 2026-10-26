package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

func clientKey(t *testing.T) (gossh.Signer, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pubKey, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return signer, strings.TrimSpace(string(gossh.MarshalAuthorizedKey(pubKey)))
}

func dialTarget(t *testing.T, addr string, auth []gossh.AuthMethod) (*gossh.Client, error) {
	t.Helper()
	return gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:            "any",
		Auth:            auth,
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
}

func TestEndToEndShell(t *testing.T) {
	signer, pubLine := clientKey(t)
	srv := newServer(config{
		shell:                defaultShell(),
		allowedClientPubKeys: []string{pubLine},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	cl, err := dialTarget(t, ln.Addr().String(), []gossh.AuthMethod{gossh.PublicKeys(signer)})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	sess, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.RequestPty("xterm", 40, 120, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stdin, "echo TUNNEL_OK_$((7*6))\n"); err != nil {
		t.Fatal(err)
	}

	out, err := readUntilMark(t, stdout, "TUNNEL_OK_42", 10*time.Second)
	if err != nil {
		t.Fatalf("%v, got: %q", err, out)
	}
}

func TestAuthRejected(t *testing.T) {
	_, pubLine := clientKey(t)
	srv := newServer(config{
		shell:                defaultShell(),
		allowedClientPubKeys: []string{pubLine},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	wrongSigner, _ := clientKey(t)
	cases := map[string][]gossh.AuthMethod{
		"wrong key":      {gossh.PublicKeys(wrongSigner)},
		"no key":         nil,
		"empty password": {gossh.Password("")},
	}
	for name, auth := range cases {
		cl, err := dialTarget(t, ln.Addr().String(), auth)
		if err == nil {
			cl.Close()
			t.Fatalf("%s: server accepted the connection", name)
		}
		if !strings.Contains(err.Error(), "unable to authenticate") {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
	}
}

func TestBakedMaterial(t *testing.T) {
	signer := mustLoadBakedSigner(relayPrivKeyB64)
	// relay private key vs host key line: just verify parsing
	if signer.PublicKey().Type() != gossh.KeyAlgoED25519 {
		t.Fatalf("relay key is not ed25519: %s", signer.PublicKey().Type())
	}
	hostSigner := mustLoadBakedSigner(serverHostKeyB64)
	if hostSigner.PublicKey().Type() != gossh.KeyAlgoED25519 {
		t.Fatalf("host key is not ed25519: %s", hostSigner.PublicKey().Type())
	}
	pub, err := bakedRelayHostKey()
	if err != nil {
		t.Fatal(err)
	}
	if pub.Type() != gossh.KeyAlgoED25519 {
		t.Fatalf("relay host key is not ed25519: %s", pub.Type())
	}
	if len(allowedClientPubKeys) == 0 || !strings.HasPrefix(allowedClientPubKeys[0], "ssh-") {
		t.Fatalf("bad client key: %q", allowedClientPubKeys)
	}
	if defaultRelay == "" || defaultRelayUser == "" {
		t.Fatal("relay constants are not set")
	}
}

func TestServerAddr(t *testing.T) {
	if got := newServerAddr(config{relay: "x", localPort: 2222}); got != "127.0.0.1:2222" {
		t.Fatalf("with a tunnel the address must be loopback, got %s", got)
	}
	if got := newServerAddr(config{relay: "", localPort: 2222}); got != "0.0.0.0:2222" {
		t.Fatalf("without a tunnel expected 0.0.0.0, got %s", got)
	}
	if srv := newServer(config{relay: "x", localPort: 2222, allowedClientPubKeys: []string{"ssh-ed25519 x"}}); srv.Addr != "127.0.0.1:2222" {
		t.Fatalf("new server did not get the address: %s", srv.Addr)
	}
}

// readUntilMark reads from r incrementally until mark appears or d expires.
func readUntilMark(t *testing.T, r io.Reader, mark string, d time.Duration) (string, error) {
	t.Helper()
	deadline := time.Now().Add(d)
	var out []byte
	buf := make([]byte, 4096)
	type res struct {
		n   int
		err error
	}
	for {
		ch := make(chan res, 1)
		go func() {
			n, err := r.Read(buf)
			ch <- res{n, err}
		}()
		select {
		case rr := <-ch:
			if rr.n > 0 {
				out = append(out, buf[:rr.n]...)
				if strings.Contains(string(out), mark) {
					return string(out), nil
				}
			}
			if rr.err != nil {
				return string(out), rr.err
			}
		case <-time.After(time.Until(deadline)):
			return string(out), io.EOF
		}
	}
}

func TestForward(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()

	tunnelEnd, clientEnd := net.Pipe()
	go forward(tunnelEnd, echoLn.Addr().String())

	go func() {
		io.WriteString(tunnelEnd, "ping\n")
	}()

	out, err := readUntilMark(t, clientEnd, "ping", 5*time.Second)
	if err != nil {
		t.Fatalf("echo did not return: %v, output: %q", err, out)
	}
}

func TestSftpUploadDownload(t *testing.T) {
	signer, pubLine := clientKey(t)
	srv := newServer(config{
		shell:                defaultShell(),
		allowedClientPubKeys: []string{pubLine},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	cl, err := dialTarget(t, ln.Addr().String(), []gossh.AuthMethod{gossh.PublicKeys(signer)})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()

	sftpCl, err := sftp.NewClient(cl)
	if err != nil {
		t.Fatal(err)
	}
	defer sftpCl.Close()

	dir := t.TempDir()
	upPath := filepath.Join(dir, "uploaded.txt")
	payload := "portassh file upload ok"
	f, err := sftpCl.Create(upPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := sftpCl.Open(upPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if string(got) != payload {
		t.Fatalf("round-trip mismatch: %q != %q", got, payload)
	}
	if st, err := sftpCl.Stat(upPath); err != nil || st.Size() != int64(len(payload)) {
		t.Fatalf("stat: %v %+v", err, st)
	}
}

func TestRegistryLineAndRoleDefaults(t *testing.T) {
	when := time.Date(2026, 10, 24, 15, 4, 0, 0, time.Local)
	line := registryLine("My-PC.1", 2200, roleUser, when)
	fields := strings.Fields(line)
	if len(fields) != 4 {
		t.Fatalf("registry line must have 4 fields: %q", line)
	}
	if fields[0] != "My-PC.1" || fields[1] != "2200" || fields[3] != "user" {
		t.Fatalf("bad registry line: %q", line)
	}
	if !strings.HasSuffix(registryLine("pc", 2201, roleAdmin, when), " admin") {
		t.Fatal("admin role missing from the line")
	}
	// a newline in the name must not break the registry format
	if bad := registryLine("evil\nline", 2200, roleUser, when); strings.ContainsAny(bad, "\n\r") {
		t.Fatalf("newline injection: %q", bad)
	}

	// role defaults
	if rp, lp := applyRoleDefaults(false, defaultRPort, defaultLPort); rp != defaultRPort || lp != defaultLPort {
		t.Fatal("user instance ports must not change")
	}
	if rp, lp := applyRoleDefaults(true, defaultRPort, defaultLPort); rp != defaultRPortAdmin || lp != defaultLPortAdmin {
		t.Fatalf("wrong admin defaults: %d/%d", rp, lp)
	}
	if rp, lp := applyRoleDefaults(true, 3000, 3001); rp != 3000 || lp != 3001 {
		t.Fatal("explicit user ports were overwritten")
	}
}

func TestGoSafeRecovers(t *testing.T) {
	done := make(chan struct{})
	goSafe("test", func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
		// panic swallowed — process alive
	case <-time.After(3 * time.Second):
		t.Fatal("goSafe did not finish")
	}
}

func TestExecRawCommandQuoting(t *testing.T) {
	signer, pubLine := clientKey(t)
	srv := newServer(config{
		shell:                defaultShell(),
		allowedClientPubKeys: []string{pubLine},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	cl, err := dialTarget(t, ln.Addr().String(), []gossh.AuthMethod{gossh.PublicKeys(signer)})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	sess, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(`echo "x  y"`)
	if err != nil {
		t.Fatal(err)
	}
	// RawCommand arrives 1:1: the double space inside quotes is preserved
	// (a shlex-rejoin would have collapsed it to one space)
	if !strings.Contains(string(out), "x  y") {
		t.Fatalf("quotes were lost: %q", string(out))
	}
}

func TestExecSpawnFailureExit127(t *testing.T) {
	old := spawnRetryDelay
	spawnRetryDelay = time.Millisecond
	defer func() { spawnRetryDelay = old }()

	signer, pubLine := clientKey(t)
	srv := newServer(config{
		shell:                "no_such_shell_12345",
		allowedClientPubKeys: []string{pubLine},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()

	cl, err := dialTarget(t, ln.Addr().String(), []gossh.AuthMethod{gossh.PublicKeys(signer)})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	sess, err := cl.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	err = sess.Run("echo hi")
	var ee *gossh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 127 {
		t.Fatalf("expected exit 127, got: %v", err)
	}
}

func TestSplitExecOpts(t *testing.T) {
	d, det, cmd := splitExecOpts("timeout=5 notepad &")
	if d != 5*time.Second || !det || cmd != "notepad" {
		t.Fatalf("timeout+& parse: %v %v %q", d, det, cmd)
	}
	d, det, cmd = splitExecOpts("dir /b &")
	if d != 0 || !det || cmd != "dir /b" {
		t.Fatalf("detached parse: %v %v %q", d, det, cmd)
	}
	d, det, cmd = splitExecOpts("  timeout=90 dir  ")
	if d != 90*time.Second || det || cmd != "dir" {
		t.Fatalf("timeout parse: %v %v %q", d, det, cmd)
	}
	d, det, cmd = splitExecOpts("plain command")
	if d != 0 || det || cmd != "plain command" {
		t.Fatalf("plain parse: %v %v %q", d, det, cmd)
	}
	if _, _, cmd := splitExecOpts("timeout=abc dir"); cmd != "timeout=abc dir" {
		t.Fatalf("bad timeout must stay as-is: %q", cmd)
	}
}
