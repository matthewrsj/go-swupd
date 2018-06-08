package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/clearlinux/mixer-tools/swupd"
)

var fromFiles = make(map[string]*swupd.File)
var fromHashes = make(map[string]*swupd.File)
var files = make(map[string]*swupd.File)
var toFiles = make(map[string]*swupd.File)
var toHashes = make(map[string]*swupd.File)
var vers = make(map[uint32]bool)

func addVer(ver interface{}) {
	switch v := ver.(type) {
	case uint32:
		vers[v] = true
	case string:
		v64, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return
		}
		vers[uint32(v64)] = true
	}
}

func getCurrentVersion() (string, error) {
	c, err := ioutil.ReadFile("/usr/lib/os-release")
	if err != nil {
		return "", err
	}

	re := regexp.MustCompile(`VERSION_ID=(\d+)\n`)
	m := re.FindStringSubmatch(string(c))
	if len(m) == 0 {
		return "", errors.New("unable to determine OS version")
	}

	return m[1], nil
}

func getCurrentFormat() (string, error) {
	c, err := ioutil.ReadFile("/usr/share/defaults/swupd/format")
	if err != nil {
		return "", err
	}

	return string(c), nil
}

func getServerVersion(format string) (string, error) {
	resp, err := http.Get("https://download.clearlinux.org/update/version/format" + format + "/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return strings.Trim(string(body), "\n"), nil
}

func getSubbedBundles() ([]string, error) {
	fs, err := ioutil.ReadDir("/usr/share/clear/bundles")
	if err != nil {
		return []string{}, err
	}

	var bundles []string
	for _, f := range fs {
		if strings.HasPrefix(f.Name(), ".") {
			continue
		}
		bundles = append(bundles, f.Name())
	}
	return bundles, nil
}

func getUpdatedBundles(bundles []string, newMoM *swupd.Manifest, currentVersion string) []*swupd.File {
	var bundlesNeeded []*swupd.File
	ver, err := strconv.ParseUint(currentVersion, 10, 32)
	if err != nil {
		fmt.Println("shoot")
	}
	addVer(ver)
	for _, man := range newMoM.Files {
		if man.Version <= uint32(ver) {
			continue
		}
		i := sort.SearchStrings(bundles, man.Name)
		if i >= len(bundles) || bundles[i] != man.Name {
			continue
		}
		bundlesNeeded = append(bundlesNeeded, man)
	}
	return bundlesNeeded
}

func consolidateAllFiles(fs map[string]*swupd.File, fhashes map[string]*swupd.File, bMan *swupd.Manifest, ver uint32) {
	for _, f := range bMan.Files {
		if ver > 0 && f.Version <= uint32(ver) {
			continue
		}

		fs[f.Name] = f
		if fhashes != nil {
			fhashes[f.Hash.String()] = f
		}
	}
}

func consolidateFiles(fs map[string]*swupd.File, fhashes map[string]*swupd.File, bMan *swupd.Manifest, ver uint32) {
	for _, f := range bMan.Files {
		if ver > 0 && f.Version <= uint32(ver) {
			continue
		}

		if !f.Present() {
			continue
		}

		fs[f.Name] = f
		if fhashes != nil {
			fhashes[f.Hash.String()] = f
		}
	}
}

func verifyUpdateFiles() error {
	for _, f := range toFiles {
		if !f.Present() {
			continue
		}
		p := filepath.Join(fmt.Sprint(f.Version), "staged", f.Hash.String())
		if _, err := os.Lstat(p); err != nil {
			fmt.Println(f)
		}
		toHash, err := swupd.Hashcalc(p)
		if err != nil {
			return err
		}

		if toHash != f.Hash {
			return err
		}
	}

	return nil
}

func stageFiles(toKeys []string) error {
	for _, k := range toKeys {
		f := toFiles[k]
		if !f.Present() {
			continue
		}
		src := filepath.Join(fmt.Sprint(f.Version), "staged", f.Hash.String())
		var dst string
		if f.Type != swupd.TypeDirectory {
			dst = filepath.Join(filepath.Dir(f.Name), ".update."+filepath.Base(f.Name))
		} else {
			dst = f.Name
		}

		if err := cpy(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func renameToFinal(toKeys []string) {
	for _, k := range toKeys {
		f := toFiles[k]
		if !f.Present() {
			if err := os.RemoveAll(f.Name); err != nil {
				fmt.Println(err)
			}
			continue
		}
		if f.Type == swupd.TypeDirectory {
			// already done
			continue
		}
		src := filepath.Join(filepath.Dir(f.Name), ".update."+filepath.Base(f.Name))
		dst := f.Name
		if err := os.Rename(src, dst); err != nil {
			fmt.Println(err)
		}
	}
}

func Update() error {
	currentVersion, err := getCurrentVersion()
	if err != nil {
		return err
	}
	fmt.Println(currentVersion)

	currentFormat, err := getCurrentFormat()
	if err != nil {
		return err
	}

	serverVersion, err := getServerVersion(currentFormat)
	if err != nil {
		return err
	}
	fmt.Println(serverVersion)

	bundles, err := getSubbedBundles()
	if err != nil {
		return err
	}
	sort.Strings(bundles)

	oldMoM, err := downloadVerifyMoM(currentVersion)
	if err != nil {
		return err
	}

	err = downloadCurrentBundles(oldMoM, bundles)
	if err != nil {
		return err
	}

	newMoM, err := downloadVerifyMoM(serverVersion)
	if err != nil {
		return err
	}

	bundlesNeeded := getUpdatedBundles(bundles, newMoM, currentVersion)

	if err = downloadVerifyBundles(bundlesNeeded, serverVersion, currentVersion, oldMoM); err != nil {
		return err
	}

	cv, err := strconv.ParseUint(currentVersion, 10, 32)
	if err != nil {
		return err
	}
	vers[uint32(cv)] = false

	if err = applyDeltas(); err != nil {
		return err
	}

	if err = downloadRemainingFiles(); err != nil {
		return err
	}

	if err = verifyUpdateFiles(); err != nil {
		return err
	}

	var toKeys []string
	for k := range toFiles {
		toKeys = append(toKeys, k)
	}
	sort.Strings(toKeys)
	for _, k := range toKeys {
		fmt.Println(toFiles[k].Name)
	}
	if err = stageFiles(toKeys); err != nil {
		return err
	}

	// CRITICAL POINT
	// NO HARD FAILURES ALLOWED
	renameToFinal(toKeys)

	return nil
}

func main() {
	if err := Update(); err != nil {
		fmt.Println(err)
	}
}
