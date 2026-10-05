// Package dotenv loads a .env file into the process environment.
package dotenv

import (
	"bufio"
	"os"
	"strings"
)

// Load reads KEY=VALUE lines from path into the process environment. Variables
// already set in the real environment, and empty values, are left alone. A missing file
// is not an error.
func Load(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(strings.TrimPrefix(k, "export ")), strings.Trim(strings.TrimSpace(v), `"'`)
		if !ok || k == "" || v == "" {
			continue
		}
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
}
