//go:build !windows

package main

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

func hideWindow(cmd *exec.Cmd) {}

func opener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

func openBrowser(url string) error { return exec.Command(opener(), url).Start() }

func openPath(path string) error { return exec.Command(opener(), path).Start() }

// pickFolder uses whichever desktop dialog tool is installed.
func pickFolder(start string) (string, error) {
	var cmd *exec.Cmd
	if p, err := exec.LookPath("kdialog"); err == nil {
		cmd = exec.Command(p, "--title", "Choose output folder", "--getexistingdirectory", start)
	} else if p, err := exec.LookPath("zenity"); err == nil {
		cmd = exec.Command(p, "--file-selection", "--directory", "--title=Choose output folder", "--filename="+start+"/")
	} else {
		return "", errors.New("no folder dialog found; type the path instead")
	}
	out, err := cmd.Output()
	if err != nil {
		return "", nil // user pressed Cancel
	}
	return strings.TrimSpace(string(out)), nil
}

func showError(msg string) {
	if p, err := exec.LookPath("kdialog"); err == nil {
		exec.Command(p, "--error", msg).Run()
	} else if p, err := exec.LookPath("zenity"); err == nil {
		exec.Command(p, "--error", "--text="+msg).Run()
	}
}
