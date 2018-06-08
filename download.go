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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/clearlinux/mixer-tools/swupd"
)

func downloadRemainingFiles() error {
	var err error
	fmt.Printf("%d files were not in a pack\n", len(files))
	for _, f := range files {
		err = os.MkdirAll(fmt.Sprintf("%d/staged", f.Version), 0744)
		if err != nil {
			return err
		}
		target := fmt.Sprintf("%d/staged/%s", f.Version, f.Name)
		if _, err = os.Lstat(target); err == nil {
			continue
		}
		target += ".tar"
		url := fmt.Sprintf("https://download.clearlinux.org/update/%d/files/%s.tar", f.Version, f.Name)
		err = tarExtractURL(url, target)
		if err != nil {
			return err
		}
	}
	return nil
}

func downloadCurrentBundles(MoM *swupd.Manifest, bundles []string) error {
	for _, m := range MoM.Files {
		i := sort.SearchStrings(bundles, m.Name)
		if i >= len(bundles) || bundles[i] != m.Name {
			continue
		}
		bMan, err := downloadManifest(m)
		if err != nil {
			return err
		}
		consolidateFiles(fromFiles, fromHashes, bMan, 0)
	}
	return nil
}

func downloadVerifyBundles(bundles []*swupd.File, serverVersion, currentVersion string, oldMoM *swupd.Manifest) error {
	ver, err := strconv.ParseUint(currentVersion, 10, 32)
	if err != nil {
		return err
	}
	for _, b := range bundles {
		bMan, err := downloadManifest(b)
		if err != nil {
			return err
		}
		consolidateAllFiles(toFiles, toHashes, bMan, uint32(ver))
		if err = downloadBundlePack(bMan, oldMoM); err != nil {
			fmt.Println("fullfile fallback", bMan.Name)
			consolidateFiles(files, nil, bMan, uint32(ver))
		}
	}

	return nil
}

func downloadVerifyMoM(serverVersion string) (*swupd.Manifest, error) {
	addVer(serverVersion)
	err := os.MkdirAll(serverVersion, 0744)
	if err != nil {
		return nil, err
	}
	outMoM, err := os.Create(filepath.Join(serverVersion, "Manifest.MoM"))
	if err != nil {
		return nil, err
	}
	resp, err := http.Get("https://download.clearlinux.org/update/" + serverVersion + "/Manifest.MoM")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	_, err = io.Copy(outMoM, resp.Body)
	if err != nil {
		return nil, err
	}

	return swupd.ParseManifestFile(filepath.Join(serverVersion, "Manifest.MoM"))
}

func downloadManifest(bundle *swupd.File) (*swupd.Manifest, error) {
	outMan := fmt.Sprintf("%d/Manifest.%s", bundle.Version, bundle.Name)
	if _, err := os.Lstat(outMan); err == nil {
		return swupd.ParseManifestFile(outMan)
	}
	url := fmt.Sprintf("https://download.clearlinux.org/update/%d/Manifest.%s.tar", bundle.Version, bundle.Name)

	addVer(bundle.Version)
	err := os.MkdirAll(fmt.Sprint(bundle.Version), 0744)
	if err != nil {
		return nil, err
	}
	err = tarExtractURL(url, outMan)
	if err != nil {
		return nil, err
	}

	return swupd.ParseManifestFile(outMan)
}

func downloadBundlePack(b *swupd.Manifest, oldMoM *swupd.Manifest) error {
	var recentVersion uint32
	for _, m := range oldMoM.Files {
		if m.Name == b.Name {
			recentVersion = m.Version
			break
		}
	}
	if recentVersion == 0 {
		return errors.New("couldn't find recent version")
	}
	outPack := fmt.Sprintf("%d/pack-%s-from-%d.tar", b.Header.Version, b.Name, recentVersion)
	if _, err := os.Lstat(outPack); err == nil {
		return nil
	}
	url := fmt.Sprintf("https://download.clearlinux.org/update/%d/pack-%s-from-%d.tar", b.Header.Version, b.Name, recentVersion)
	return tarExtractURL(url, outPack)
}

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

func tarExtractURL(url, target string) error {
	if err := Download(url, target); err != nil {
		return err
	}

	return RunCommandSilent("tar", "-C", filepath.Dir(target), "-xf", target)
}
