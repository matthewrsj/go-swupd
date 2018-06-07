package main

import (
	"errors"
	"fmt"
	"io"
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
var files = make(map[string]*swupd.File)
var toFiles = make(map[string]*swupd.File)
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

func getUpdatedBundles(bundles []string, newMoM *swupd.Manifest, currentVersion string) []*swupd.File {
	var bundlesNeeded []*swupd.File
	ver, err := strconv.ParseUint(currentVersion, 10, 32)
	if err != nil {
		fmt.Println("shit")
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

func consolidateFiles(fs map[string]*swupd.File, bMan *swupd.Manifest, ver uint32) {
	for _, f := range bMan.Files {
		if ver > 0 && f.Version <= uint32(ver) {
			continue
		}

		if !f.Present() {
			continue
		}

		fs[f.Hash.String()] = f
	}
}

func downloadRemainingFiles() error {
	var err error
	fmt.Printf("%d files were not in a pack\n", len(files))
	for name, f := range files {
		err = os.MkdirAll(fmt.Sprintf("%d/staged", f.Version), 0744)
		if err != nil {
			return err
		}
		target := fmt.Sprintf("%d/staged/%s", f.Version, name)
		if _, err = os.Lstat(target); err == nil {
			continue
		}
		target += ".tar"
		url := fmt.Sprintf("https://download.clearlinux.org/update/%d/files/%s.tar", f.Version, name)
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
		consolidateFiles(fromFiles, bMan, 0)
	}
	return nil
}

func downloadVerifyBundles(bundles []*swupd.File, serverVersion, currentVersion string, oldMoM *swupd.Manifest) error {
	//var err error
	ver, err := strconv.ParseUint(currentVersion, 10, 32)
	if err != nil {
		return err
	}
	for _, b := range bundles {
		bMan, err := downloadManifest(b)
		if err != nil {
			return err
		}
		consolidateFiles(toFiles, bMan, uint32(ver))
		if err = downloadBundlePack(bMan, oldMoM); err != nil {
			fmt.Println("fullfile fallback", bMan.Name)
			consolidateFiles(files, bMan, uint32(ver))
		}
	}

	return nil
}

func applyDelta(from *swupd.File, to *swupd.File, deltaPath string) error {
	if _, err := os.Lstat(from.Name); err != nil {
		return err
	}

	outPath := filepath.Join(fmt.Sprint(to.Version), "staged", to.Hash.String())
	if _, err := os.Lstat(outPath); err == nil {
		return nil
	}

	if err := RunCommandSilent("bspatch", from.Name, outPath+".test", deltaPath); err != nil {
		files[to.Hash.String()] = to
		return err
	}
	defer func() {
		_ = os.Rename(outPath+".test", outPath)
	}()

	testHash, err := swupd.Hashcalc(outPath + ".test")
	if err != nil {
		_ = os.Remove(outPath)
		files[to.Hash.String()] = to
		return err
	}

	if testHash != to.Hash {
		_ = os.Remove(outPath)
		files[to.Hash.String()] = to
		return err
	}

	return nil
}

func applyDeltasFromVersion(v uint32) error {
	deltaDir := filepath.Join(fmt.Sprint(v), "delta")
	if _, err := os.Lstat(deltaDir); err != nil {
		return nil
	}

	return filepath.Walk(deltaDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// <fromver>-<tover>-<fromhash>-<tohash>
		fields := strings.Split(info.Name(), "-")
		if len(fields) != 4 {
			return nil
		}
		fromFile := fromFiles[fields[2]]
		toFile := toFiles[fields[3]]
		err = applyDelta(fromFile, toFile, path)
		if err != nil {
			fmt.Println(err)
		}
		return nil
	})
}

func applyDeltas() error {
	for v, b := range vers {
		if !b {
			continue
		}
		fmt.Println(v)
		err := applyDeltasFromVersion(v)
		if err != nil {
			return err
		}
	}
	return nil
}

func verifyUpdateFiles() error {
	for _, f := range toFiles {
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

func stageFiles() error {
	for _, f := range toFiles {
		src := filepath.Join(fmt.Sprint(f.Version), "staged", f.Hash.String())
		dst := filepath.Join(filepath.Dir(f.Name), ".update."+filepath.Base(f.Name))
		err := os.Link(src, dst)
		if err != nil {
			return err
		}
	}
	return nil
}

func renameToFinal() error {
	return nil
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
	fmt.Println(currentFormat)

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
	fmt.Println(bundles)

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

	fmt.Println(newMoM.Header)
	fmt.Println(oldMoM.Header)

	bundlesNeeded := getUpdatedBundles(bundles, newMoM, currentVersion)
	for _, b := range bundlesNeeded {
		fmt.Println(b.Name, b.Version)
	}

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

	if err = stageFiles(); err != nil {
		return err
	}

	// CRITICAL POINT
	// NO HARD FAILURES ALLOWED
	renameToFinal()

	return nil
}

func main() {
	if err := Update(); err != nil {
		fmt.Println(err)
	}
}
