// Copyright © 2018 Intel Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// download does a simple http.Get on the url and performs a check against the
// error code. The response body is only returned for StatusOK
func download(url string) (*http.Response, error) {
	resp, err := http.Get(url)
	if err != nil {
		return &http.Response{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Get %s replied: %d (%s)",
			url, resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return resp, nil
}

// Download will attempt to download a from URL to the given filename. Does not
// try to extract the file, simply lays it on disk. Use this function if you
// know the file at url is not compressed or if you want to download a
// compressed file as-is.
func Download(url, filename string) error {
	resp, err := download(url)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// write to a temporary file so if the process is aborted the user is
	// not left with a truncated file
	tmpFile := filepath.Join(filepath.Dir(filename), ".dl."+filepath.Base(filename))
	out, err := os.Create(tmpFile)
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(tmpFile)
	}()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	// move tempfile to final now that everything else has succeeded
	return os.Rename(tmpFile, filename)
}

// DownloadFile downloads from url and extracts the file if necessary using the
// compression method indicated by the url file extension. If there is no file
// extension or the extension does not match a supported compression method the
// file is downloaded as-is.
func DownloadFile(url, target string) error {
	var err error
	switch filepath.Ext(url) {
	case ".gz":
		err = gzExtractURL(url, target)
	case ".xz":
		err = xzExtractURL(url, target)
	default:
		err = Download(url, target)
	}
	return err
}

// gzExtractURL will download a file at the url and extract it to the target
// location
func gzExtractURL(url, target string) error {
	resp, err := download(url)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}

	out, err := os.Create(target)
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
	}()

	_, ioerr := io.Copy(out, zr)
	if ioerr != nil {
		if err := os.RemoveAll(target); err != nil {
			return errors.New(ioerr.Error() + err.Error())
		}
		return ioerr
	}

	return nil
}

func xzExtractURL(url, target string) error {
	// download to file, no native xz compression library in Go
	if err := Download(url, target+".xz"); err != nil {
		return err
	}

	return RunCommandSilent("unxz", "-T", "0", target+".xz")
}

// tty tracks whether stdout is to a TTY
// -1 means unset
// 0 means this is a tty
// 1 means this is not a tty
var tty = -1

// StdoutIsTTY returns true if Stdout is to a TTY
func StdoutIsTTY() bool {
	// check for cached result
	if tty != -1 {
		return tty == 0
	}

	_, _, err := syscall.Syscall6(
		syscall.SYS_IOCTL,
		1, // stdout file descriptor
		syscall.TCGETS,
		uintptr(unsafe.Pointer(&syscall.Termios{})),
		0, 0, 0,
	)

	// set cache so we don't syscall every time
	// err (errno) can be -1 so don't set tty directly to err
	if err == 0 {
		tty = 0
	} else {
		tty = 1
	}

	return tty == 0
}

// RunCommandSilent runs the given command with args and does not print output
func RunCommandSilent(cmdname string, args ...string) error {
	_, err := RunCommandOutput(cmdname, args...)
	return err
}

// RunCommandOutput executes the command with arguments and stores its output in
// memory. If the command succeeds returns that output, if it fails, return err that
// contains both the out and err streams from the execution.
func RunCommandOutput(cmdname string, args ...string) (*bytes.Buffer, error) {
	cmd := exec.Command(cmdname, args...)
	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "failed to execute %s", strings.Join(cmd.Args, " "))
		if outBuf.Len() > 0 {
			fmt.Fprintf(&buf, "\nSTDOUT:\n%s", outBuf.Bytes())
		}
		if errBuf.Len() > 0 {
			fmt.Fprintf(&buf, "\nSTDERR:\n%s", errBuf.Bytes())
		}
		if outBuf.Len() > 0 || errBuf.Len() > 0 {
			// Finish without a newline to wrap well with the err.
			fmt.Fprintf(&buf, "failed to execute")
		}
		return &outBuf, errors.New(err.Error() + buf.String())
	}
	return &outBuf, nil
}

func tarExtractURL(url, target string) error {
	if err := Download(url, target); err != nil {
		return err
	}

	return RunCommandSilent("tar", "-C", filepath.Dir(target), "-xf", target)
}

// Fail prints the error and exits the program with an error code
func Fail(err error) {
	fmt.Fprintf(os.Stderr, "%s: ERROR: %s\n", os.Args[0], err)
	os.Exit(1)
}
