package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	errorMoreData    = 234
	errorNoMoreItems = 259
)

var (
	system32        = filepath.Join(os.Getenv("SystemRoot"), "System32")
	msi             = syscall.NewLazyDLL(filepath.Join(system32, "msi.dll"))
	msiOpenDatabase = msi.NewProc("MsiOpenDatabaseW")
	msiOpenView     = msi.NewProc("MsiDatabaseOpenViewW")
	msiViewExecute  = msi.NewProc("MsiViewExecute")
	msiViewFetch    = msi.NewProc("MsiViewFetch")
	msiGetString    = msi.NewProc("MsiRecordGetStringW")
	msiDataSize     = msi.NewProc("MsiRecordDataSize")
	msiReadStream   = msi.NewProc("MsiRecordReadStream")
	msiCloseHandle  = msi.NewProc("MsiCloseHandle")
)

func call(proc *syscall.LazyProc, args ...uintptr) error {
	if code, _, _ := proc.Call(args...); code != 0 {
		return fmt.Errorf("%s failed with code %d", proc.Name, code)
	}
	return nil
}

func eachRow(db uint32, query string, visit func(rec uint32) error) error {
	q, _ := syscall.UTF16PtrFromString(query)
	var view uint32
	if err := call(msiOpenView, uintptr(db), uintptr(unsafe.Pointer(q)), uintptr(unsafe.Pointer(&view))); err != nil {
		return err
	}
	defer msiCloseHandle.Call(uintptr(view))
	if err := call(msiViewExecute, uintptr(view), 0); err != nil {
		return err
	}
	for {
		var rec uint32
		code, _, _ := msiViewFetch.Call(uintptr(view), uintptr(unsafe.Pointer(&rec)))
		if code == errorNoMoreItems {
			return nil
		}
		if code != 0 {
			return fmt.Errorf("MsiViewFetch failed with code %d", code)
		}
		err := visit(rec)
		msiCloseHandle.Call(uintptr(rec))
		if err != nil {
			return err
		}
	}
}

func field(rec uint32, index int) string {
	var size uint32
	empty := [1]uint16{}
	code, _, _ := msiGetString.Call(uintptr(rec), uintptr(index), uintptr(unsafe.Pointer(&empty[0])), uintptr(unsafe.Pointer(&size)))
	if code != errorMoreData {
		return ""
	}
	size++
	buf := make([]uint16, size)
	msiGetString.Call(uintptr(rec), uintptr(index), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	return syscall.UTF16ToString(buf)
}

// MSI names are "short|long", and DefaultDir may add ":source".
func longName(name string) string {
	name, _, _ = strings.Cut(name, ":")
	if _, long, ok := strings.Cut(name, "|"); ok {
		return long
	}
	return name
}

func safeName(name string) bool {
	return name != "" && name != ".." && !strings.ContainsAny(name, `\/:*?"<>|`)
}

type directory struct{ parent, name string }

// Unpacks every file under INSTALLFOLDER without running the installer, so no elevation is involved.
func extractMSI(msiPath, dest, work string) error {
	path, _ := syscall.UTF16PtrFromString(msiPath)
	var db uint32
	if err := call(msiOpenDatabase, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&db))); err != nil {
		return err
	}
	defer msiCloseHandle.Call(uintptr(db))

	dirs := map[string]directory{}
	err := eachRow(db, "SELECT `Directory`, `Directory_Parent`, `DefaultDir` FROM `Directory`", func(rec uint32) error {
		dirs[field(rec, 1)] = directory{field(rec, 2), longName(field(rec, 3))}
		return nil
	})
	if err != nil {
		return err
	}
	relDir := func(key string) (string, bool) {
		var parts []string
		for key != "INSTALLFOLDER" {
			d, ok := dirs[key]
			if !ok || d.parent == "" {
				return "", false
			}
			if d.name != "." {
				parts = append([]string{d.name}, parts...)
			}
			key = d.parent
		}
		return filepath.Join(parts...), true
	}

	componentDirs := map[string]string{}
	err = eachRow(db, "SELECT `Component`, `Directory_` FROM `Component`", func(rec uint32) error {
		componentDirs[field(rec, 1)] = field(rec, 2)
		return nil
	})
	if err != nil {
		return err
	}

	unpacked := filepath.Join(work, "cab")
	if err := os.MkdirAll(unpacked, 0o755); err != nil {
		return err
	}
	var cabs []string
	err = eachRow(db, "SELECT `Cabinet` FROM `Media`", func(rec uint32) error {
		if name, embedded := strings.CutPrefix(field(rec, 1), "#"); embedded {
			cabs = append(cabs, name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, cab := range cabs {
		cabPath := filepath.Join(work, "stream.cab")
		err = eachRow(db, "SELECT `Data` FROM `_Streams` WHERE `Name` = '"+cab+"'", func(rec uint32) error {
			size, _, _ := msiDataSize.Call(uintptr(rec), 1)
			data := make([]byte, size)
			n := uint32(size)
			if err := call(msiReadStream, uintptr(rec), 1, uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&n))); err != nil {
				return err
			}
			return os.WriteFile(cabPath, data[:n], 0o644)
		})
		if err != nil {
			return err
		}
		out, err := exec.Command(filepath.Join(system32, "expand.exe"), cabPath, "-F:*", unpacked).CombinedOutput()
		if err != nil {
			return fmt.Errorf("expand.exe: %w\n%s", err, out)
		}
	}

	return eachRow(db, "SELECT `File`, `Component_`, `FileName` FROM `File`", func(rec uint32) error {
		key, name := field(rec, 1), longName(field(rec, 3))
		rel, ok := relDir(componentDirs[field(rec, 2)])
		if !ok {
			return nil
		}
		for _, part := range append(strings.Split(rel, string(filepath.Separator)), name) {
			if part != "" && !safeName(part) {
				return fmt.Errorf("unsafe path in MSI: %s", filepath.Join(rel, name))
			}
		}
		target := filepath.Join(dest, rel, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.Rename(filepath.Join(unpacked, key), target)
	})
}
