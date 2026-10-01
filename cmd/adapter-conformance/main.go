// Command adapter-conformance checks an adapter executable using only its
// black-box Protocol v1 process interface.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

func main() {
	if err := command(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}

func command(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("adapter-conformance", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	binary := flags.String("binary", "", "adapter executable to validate")
	jsonOutput := flags.Bool("json", false, "write a machine-readable report")
	timeout := flags.Duration("timeout", 10*time.Second, "deadline for each adapter operation")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintln(stderr, "invalid command arguments")
		return errors.New("invalid command arguments")
	}
	if flags.NArg() != 0 || *binary == "" || *timeout <= 0 || *timeout > 5*time.Minute {
		fmt.Fprintln(stderr, "usage: adapter-conformance --binary PATH [--json] [--timeout DURATION]")
		return errors.New("invalid command arguments")
	}
	report := runConformance(*binary, *timeout)
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(true)
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintln(stderr, "could not write conformance report")
			return err
		}
	} else {
		for _, check := range report.Checks {
			state := "PASS"
			if !check.Passed {
				state = "FAIL"
			}
			fmt.Fprintf(stdout, "%s %s", state, check.Name)
			if check.Error != "" {
				fmt.Fprintf(stdout, ": %s", check.Error)
			}
			fmt.Fprintln(stdout)
		}
		if report.AdapterID != "" {
			fmt.Fprintf(stdout, "adapter_id: %s\n", report.AdapterID)
		}
	}
	if !report.Passed {
		return errors.New("adapter conformance failed")
	}
	return nil
}
