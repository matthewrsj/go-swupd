package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/clearlinux/mixer-tools/swupd"
)

func applyDelta(from *swupd.File, to *swupd.File, deltaPath string) error {
	if _, err := os.Lstat(from.Name); err != nil {
		return err
	}

	outPath := filepath.Join(fmt.Sprint(to.Version), "staged", to.Hash.String())
	if _, err := os.Lstat(outPath); err == nil {
		return nil
	}

	if err := RunCommandSilent("bspatch", from.Name, outPath+".test", deltaPath); err != nil {
		files[to.Name] = to
		return err
	}
	defer func() {
		_ = os.Rename(outPath+".test", outPath)
	}()

	testHash, err := swupd.Hashcalc(outPath + ".test")
	if err != nil {
		_ = os.Remove(outPath)
		files[to.Name] = to
		return err
	}

	if testHash != to.Hash {
		_ = os.Remove(outPath)
		files[to.Name] = to
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
		fromFile := fromHashes[fields[2]]
		toFile := toHashes[fields[3]]
		if fromFile == nil {
			return fmt.Errorf("%s fromFile is nil", path)
		}

		if toFile == nil {
			return fmt.Errorf("%s toFile is nil", path)
		}
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
