package provider

import (
	"fmt"
	"net"
	"os"
	"time"
)

func getenv(k string) string { return os.Getenv(k) }
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func waitTCP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("timeout waiting for %s", addr)
}
