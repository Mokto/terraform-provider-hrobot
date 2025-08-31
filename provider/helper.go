package provider

import "os"

func getenv(k string) string { return os.Getenv(k) }
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
