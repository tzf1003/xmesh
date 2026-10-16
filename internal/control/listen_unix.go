//go:build !windows

package control

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// maxSocketPath is below the smallest sun_path (104 bytes on macOS, 108 on Linux).
const maxSocketPath = 100

// Listen opens control.sock in the state directory (mode 0600 in a 0700 directory)
// and returns a listener that accepts only connections from the kernel's own uid.
func Listen(o ListenOptions) (net.Listener, Endpoint, error) {
	path := filepath.Join(o.StateDir, "control.sock")
	if len(path) > maxSocketPath {
		dir := filepath.Join(os.TempDir(), "xmesh-"+strconv.Itoa(os.Geteuid()))
		if err := privateDir(dir); err != nil {
			return nil, Endpoint{}, err
		}
		path = filepath.Join(dir, o.Node+".sock")
		if len(path) > maxSocketPath {
			return nil, Endpoint{}, errors.New("控制通道套接字路径过长")
		}
	}
	l, err := listenUnix(path, uint32(os.Geteuid()))
	if err != nil {
		return nil, Endpoint{}, err
	}
	return l, Endpoint{Transport: "unix", Address: path}, nil
}

// privateDir makes sure dir exists, is owned by this user, is not a symlink and
// is not accessible to anyone else.
func privateDir(dir string) error {
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !st.IsDir() || !ok || int(owner.Uid) != os.Geteuid() {
		return fmt.Errorf("控制通道目录不属于当前用户: %s", dir)
	}
	return os.Chmod(dir, 0700)
}

func listenUnix(path string, uid uint32) (net.Listener, error) {
	if err := removeStale(path); err != nil {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return &guarded{Listener: l, uid: uid}, nil
}

// removeStale deletes a socket nobody listens on, and refuses to touch a live one
// or a file that is not a socket.
func removeStale(path string) error {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s 已存在且不是套接字", path)
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		_ = c.Close()
		return fmt.Errorf("另一个 xmesh 进程已在使用 %s", path)
	}
	return os.Remove(path)
}

// guarded drops every connection whose peer credentials are not uid (CON-SEC-12).
type guarded struct {
	net.Listener
	uid uint32
}

func (g *guarded) Accept() (net.Conn, error) {
	for {
		c, err := g.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if uc, ok := c.(*net.UnixConn); ok {
			if peer, err := peerUID(uc); err == nil && peer == g.uid {
				return c, nil
			}
		}
		_ = c.Close()
	}
}
