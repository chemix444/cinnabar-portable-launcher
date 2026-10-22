package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	latestURL = "https://api.github.com/repos/cinnabar-mc/cinnabar/releases/latest"
	msiAsset  = "Cinnabar-x64.msi"
	sumsAsset = "SHA256SUMS.txt"
)

type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (r release) asset(name string) (string, error) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("release %s has no %s", r.Tag, name)
}

func get(client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "cinnabar-portable-launcher")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

type progress struct {
	done, total int64
}

func (p *progress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	fmt.Printf("\r  %.1f / %.1f MB", float64(p.done)/1e6, float64(p.total)/1e6)
	return len(b), nil
}

func download(url, path string) (string, error) {
	resp, err := get(http.DefaultClient, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, hash, &progress{total: resp.ContentLength}), resp.Body)
	fmt.Println()
	return hex.EncodeToString(hash.Sum(nil)), err
}

func expectedSum(client *http.Client, url, name string) (string, error) {
	resp, err := get(client, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("%s not listed in %s", name, sumsAsset)
}

func update(root, app string) error {
	client := &http.Client{Timeout: 20 * time.Second}
	fmt.Println("checking for updates...")
	resp, err := get(client, latestURL)
	if err != nil {
		return err
	}
	var latest release
	err = json.NewDecoder(resp.Body).Decode(&latest)
	resp.Body.Close()
	if err != nil {
		return err
	}

	versionFile := filepath.Join(app, "version.txt")
	installed, _ := os.ReadFile(versionFile)
	if strings.TrimSpace(string(installed)) == latest.Tag {
		fmt.Println("up to date:", latest.Tag)
		return nil
	}

	msiURL, err := latest.asset(msiAsset)
	if err != nil {
		return err
	}
	sumsURL, err := latest.asset(sumsAsset)
	if err != nil {
		return err
	}
	want, err := expectedSum(client, sumsURL, msiAsset)
	if err != nil {
		return err
	}

	work := filepath.Join(root, ".update")
	os.RemoveAll(work)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(work)

	fmt.Println("downloading", latest.Tag)
	msiPath := filepath.Join(work, msiAsset)
	got, err := download(msiURL, msiPath)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("checksum mismatch for %s", msiAsset)
	}

	fmt.Println("unpacking...")
	next := filepath.Join(root, "app.new")
	os.RemoveAll(next)
	if err := extractMSI(msiPath, next, work); err != nil {
		os.RemoveAll(next)
		return err
	}
	if _, err := os.Stat(filepath.Join(next, "cinnabar.exe")); err != nil {
		os.RemoveAll(next)
		return errors.New("update did not contain cinnabar.exe")
	}
	if err := os.WriteFile(filepath.Join(next, "version.txt"), []byte(latest.Tag+"\n"), 0o644); err != nil {
		return err
	}

	old := filepath.Join(root, "app.old")
	os.RemoveAll(old)
	if _, err := os.Stat(app); err == nil {
		if err := os.Rename(app, old); err != nil {
			os.RemoveAll(next)
			return fmt.Errorf("close cinnabar before updating: %w", err)
		}
	}
	if err := os.Rename(next, app); err != nil {
		return err
	}
	os.RemoveAll(old)
	fmt.Println("updated to", latest.Tag)
	return nil
}

func fail(err error) {
	fmt.Println("error:", err)
	fmt.Print("press Enter to close")
	bufio.NewReader(os.Stdin).ReadString('\n')
	os.Exit(1)
}

// Cinnabar's first-run setup deletes these leftovers with Rust's remove_dir_all, which opens each
// entry with OBJ_DONT_REPARSE and fails with os error 4395 on PCs whose profile folders go through
// a redirecting filter driver. Go deletes by plain path, so clearing them here sidesteps that.
func clearPrepareLeftovers(data string) {
	leftovers := []string{filepath.Join(data, "prepare", ".local", "assets", "compiled")}
	// The previous set is the only copy while the current one is missing, so keep it then.
	if _, err := os.Stat(filepath.Join(data, "assets", "compiled")); err == nil {
		leftovers = append(leftovers, filepath.Join(data, "assets", "compiled.previous"))
	}
	for _, dir := range leftovers {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Println("could not clear", dir+":", err)
		}
	}
}

func main() {
	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	root := filepath.Dir(exe)
	app := filepath.Join(root, "app")
	client := filepath.Join(app, "cinnabar.exe")

	if err := update(root, app); err != nil {
		if _, missing := os.Stat(client); missing != nil {
			fail(err)
		}
		fmt.Println("update skipped:", err)
		time.Sleep(2 * time.Second)
	}

	// Keeping user data beside the launcher makes the install portable and keeps it out of
	// %LOCALAPPDATA%, which managed PCs often redirect in ways first-run setup can't handle.
	userRoot := os.Getenv("CINNABAR_USER_ROOT")
	if userRoot == "" {
		userRoot = filepath.Join(root, "data")
	}
	clearPrepareLeftovers(filepath.Join(userRoot, "data"))

	cmd := exec.Command(client, os.Args[1:]...)
	cmd.Dir = app
	// The launcher owns updates; the client's own updater installs the per-machine MSI, which needs admin.
	cmd.Env = append(os.Environ(), "CINNABAR_UPDATE_CHECK=0", "CINNABAR_USER_ROOT="+userRoot)
	if err := cmd.Start(); err != nil {
		fail(err)
	}
}
