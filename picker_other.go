//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// pickFolder opens the desktop's folder chooser. It returns an empty string
// when the person closes the dialog without choosing, and an error only when
// the system has no chooser to offer.
func pickFolder(title string) (string, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("osascript", "-e",
			`POSIX path of (choose folder with prompt "`+strings.ReplaceAll(title, `"`, "")+`")`).Output()
		if err != nil {
			return "", nil // cancelled
		}
		return strings.TrimSpace(string(out)), nil
	}

	for _, argv := range [][]string{
		{"zenity", "--file-selection", "--directory", "--title=" + title},
		{"kdialog", "--getexistingdirectory", "."},
		{"qarma", "--file-selection", "--directory", "--title=" + title},
	} {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		out, err := exec.Command(argv[0], argv[1:]...).Output()
		if err != nil {
			return "", nil // cancelled
		}
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("máy này không có hộp thoại chọn thư mục (cài zenity hoặc kdialog), hãy dán đường dẫn vào ô bên cạnh")
}
