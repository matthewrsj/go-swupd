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

var (
	fromFiles  = make(map[string]*swupd.File)
	fromHashes = make(map[string]*swupd.File)
	files      = make(map[string]*swupd.File)
	toFiles    = make(map[string]*swupd.File)
	toHashes   = make(map[string]*swupd.File)
)

var vers = make(map[uint32]bool)

var stateDir = "/var/lib/go-swupd"

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

func getCurrentVersion() (uint32, error) {
	c, err := ioutil.ReadFile("/usr/lib/os-release")
	if err != nil {
		return 0, err
	}

	re := regexp.MustCompile(`VERSION_ID=(\d+)\n`)
	m := re.FindStringSubmatch(string(c))
	if len(m) == 0 {
		return 0, errors.New("unable to determine OS version")
	}

	ver, err := strconv.ParseUint(m[1], 10, 32)
	if err != nil {
		return 0, err
	}

	return uint32(ver), nil
}

func getCurrentFormat() (uint, error) {
	c, err := ioutil.ReadFile("/usr/share/defaults/swupd/format")
	if err != nil {
		return 0, err
	}

	f, err := strconv.ParseUint(strings.Trim(string(c), "\n"), 10, 32)
	if err != nil {
		return 0, err
	}

	return uint(f), nil
}

func getServerVersion(format uint) (uint32, error) {
	sFmt := fmt.Sprint(format)
	resp, err := http.Get("https://download.clearlinux.org/update/version/format" + sFmt + "/latest")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	verString := strings.Trim(string(body), "\n")
	v, err := strconv.ParseUint(verString, 10, 32)
	return uint32(v), err
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

func getUpdatedBundles(bundles []string, newMoM *swupd.Manifest, currentVersion uint32) []*swupd.File {
	var bundlesNeeded []*swupd.File
	addVer(currentVersion)
	for _, man := range newMoM.Files {
		if man.Version <= currentVersion {
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
		p := filepath.Join(stateDir, fmt.Sprint(f.Version), "staged", f.Hash.String())
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
		src := filepath.Join(stateDir, fmt.Sprint(f.Version), "staged", f.Hash.String())
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

	if err = downloadVerifyBundles(bundlesNeeded, oldMoM); err != nil {
		return err
	}

	vers[currentVersion] = false

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
