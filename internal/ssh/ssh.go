package ssh

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type Conn struct {
	Host                  string
	User                  string
	Timeout               time.Duration
	Auth                  Auth
	InsecureIgnoreHostKey bool
}

type Auth struct {
	pass     string
	useAgent bool
}

func AuthPassword(p string) Auth { return Auth{pass: p} }
func AuthFromAgent() Auth        { return Auth{useAgent: true} }

type Handle struct{ c *ssh.Client }

func Connect(c Conn) (*Handle, func(), error) {
	var methods []ssh.AuthMethod
	if c.Auth.useAgent {
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			if conn, err := net.Dial("unix", sock); err == nil {
				methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
			}
		}
	}
	if c.Auth.pass != "" {
		methods = append(methods, ssh.Password(c.Auth.pass))
	}
	cfg := &ssh.ClientConfig{
		User:            c.User,
		Auth:            methods,
		Timeout:         c.Timeout,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(c.Host, "22"), cfg)
	if err != nil {
		return nil, nil, err
	}
	h := &Handle{c: client}
	return h, func() { _ = client.Close() }, nil
}

func Run(h *Handle, cmd string) (string, error) {
	sess, err := h.c.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	var out, errb bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &errb
	if err := sess.Run(cmd); err != nil {
		if errb.Len() > 0 {
			return out.String(), fmt.Errorf("%v: %s", err, errb.String())
		}
		return out.String(), err
	}
	return out.String(), nil
}

func Upload(h *Handle, dst string, data []byte, _mode uint32) error {
	s, err := sftp.NewClient(h.c)
	if err != nil {
		return err
	}
	defer s.Close()
	f, err := s.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}
