package api

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// rssKB returns the process resident set size in kB (macOS, via ps).
func rssKB() int {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}
