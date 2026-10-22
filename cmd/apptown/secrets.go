package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// cmdSecret: apptown secret set <NAME> | apptown secret remove <NAME> — the
// AppTown's own credentials (a target's API token, an SSH key), administrator
// only. The value is read from standard input, never from the command line
// where the shell's history and the process list would keep it; at a
// terminal it is asked for without echo.
func cmdSecret(args []string) error {
	usage := usagef("usage: apptown secret set <NAME> < value-file  ·  apptown secret remove <NAME>")
	if len(args) != 2 {
		return usage
	}
	name := args[1]
	switch args[0] {
	case "set":
		value, err := readSecretValue(name, secretIn)
		if err != nil {
			return err
		}
		if _, err := api.request("PUT", "/secrets/"+name, map[string]string{"value": value}); err != nil {
			return err
		}
		fmt.Printf("%s AppTown holds %s (the value is not shown again)\n", green("✓"), name)
		return nil
	case "remove", "rm":
		if _, err := api.request("DELETE", "/secrets/"+name, nil); err != nil {
			return err
		}
		fmt.Printf("%s removed %s from AppTown's secrets file\n", green("✓"), name)
		return nil
	}
	return usage
}

// secretIn is where a secret's value is read from: standard input.
var secretIn = os.Stdin

// readSecretValue reads one value: hidden at a terminal, else all of
// standard input less its final line ending. A value of several lines is
// refused, since the secrets file keeps one line per secret.
func readSecretValue(name string, in *os.File) (string, error) {
	var raw []byte
	var err error
	if term.IsTerminal(int(in.Fd())) {
		fmt.Fprintf(os.Stderr, "value for %s (not echoed): ", name)
		raw, err = term.ReadPassword(int(in.Fd()))
		fmt.Fprintln(os.Stderr)
	} else {
		raw, err = io.ReadAll(io.LimitReader(in, 64<<10))
	}
	if err != nil {
		return "", err
	}
	return secretValue(string(raw))
}

func secretValue(raw string) (string, error) {
	value := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("the value has several lines; AppTown's secrets file keeps one line per secret (write an SSH key's line breaks as \\n)")
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("no value on standard input")
	}
	return value, nil
}
