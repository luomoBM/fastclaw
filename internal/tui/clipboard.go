package tui

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const maxClipboardImageBytes = 8 << 20

var errNoClipboardImage = errors.New("clipboard does not contain an image")

// clipboardImage reads an image from the native desktop clipboard and returns
// a data URL suitable for the chat API. Terminals do not put image bytes on
// stdin when the user pastes, so this has to query the OS clipboard directly.
func clipboardImage() (string, error) {
	var data []byte
	var err error
	switch runtime.GOOS {
	case "darwin":
		data, err = macOSClipboardImage()
	case "linux":
		data, err = linuxClipboardImage()
	case "windows":
		data, err = windowsClipboardImage()
	default:
		return "", fmt.Errorf("image paste is not supported on %s", runtime.GOOS)
	}
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errNoClipboardImage
	}
	if len(data) > maxClipboardImageBytes {
		return "", fmt.Errorf("clipboard image is too large (%d MiB; limit 8 MiB)", (len(data)+(1<<20)-1)/(1<<20))
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return "", errNoClipboardImage
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func macOSClipboardImage() ([]byte, error) {
	f, err := os.CreateTemp("", "fastclaw-clipboard-*.png")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	_ = f.Close()
	defer os.Remove(path)

	// PNGf coercion covers screenshots and copied browser images. Keeping the
	// file I/O inside AppleScript avoids parsing osascript's binary stdout.
	script := `on run argv
set outFile to POSIX file (item 1 of argv)
try
  set imageData to the clipboard as «class PNGf»
on error
  error "clipboard does not contain a PNG image"
end try
set fileRef to open for access outFile with write permission
try
  set eof fileRef to 0
  write imageData to fileRef
  close access fileRef
on error errMsg
  try
    close access fileRef
  end try
  error errMsg
end try
end run`
	if out, err := exec.Command("osascript", "-e", script, path).CombinedOutput(); err != nil {
		if strings.Contains(string(out), "clipboard does not contain") || strings.Contains(string(out), "Can’t make") {
			return nil, errNoClipboardImage
		}
		return nil, fmt.Errorf("read macOS clipboard: %w", err)
	}
	return os.ReadFile(path)
}

func linuxClipboardImage() ([]byte, error) {
	if _, err := exec.LookPath("wl-paste"); err == nil {
		for _, mime := range []string{"image/png", "image/jpeg", "image/webp"} {
			if data, err := exec.Command("wl-paste", "--no-newline", "--type", mime).Output(); err == nil && len(data) > 0 {
				return data, nil
			}
		}
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		for _, mime := range []string{"image/png", "image/jpeg", "image/webp"} {
			if data, err := exec.Command("xclip", "-selection", "clipboard", "-t", mime, "-o").Output(); err == nil && len(data) > 0 {
				return data, nil
			}
		}
	}
	return nil, fmt.Errorf("%w (install wl-clipboard or xclip)", errNoClipboardImage)
}

func windowsClipboardImage() ([]byte, error) {
	f, err := os.CreateTemp("", "fastclaw-clipboard-*.png")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	_ = f.Close()
	defer os.Remove(path)
	script := `Add-Type -AssemblyName System.Windows.Forms; $img=[Windows.Forms.Clipboard]::GetImage(); if ($null -eq $img) { exit 2 }; $img.Save($args[0],[Drawing.Imaging.ImageFormat]::Png)`
	if err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script, path).Run(); err != nil {
		return nil, errNoClipboardImage
	}
	return os.ReadFile(path)
}
