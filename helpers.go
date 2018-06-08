package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

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

func cpy(src, dst string) error {
	if err := os.Link(src, dst); err != nil {
		fmt.Println(err)
		if err := RunCommandSilent("cp", "-af", src, dst); err != nil {
			if strings.Contains(err.Error(), "are the same file") {
				fmt.Println("same file, skipping")
				return nil
			}
			return err
		}
	}
	return nil
}
