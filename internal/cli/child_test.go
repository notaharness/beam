//go:build beamtest

package cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// child is this test binary run as a peer's process, `child <verb> args...`,
// where no shell or system command would behave the same on every system:
//
//	argv args...     prints its arguments as JSON
//	env names...     prints name=value for each, value "(unset)" if unset
//	env-value name   prints the value of name
//	cwd              prints its working directory
//	cat              copies stdin to stdout
//	stderr text      writes text to stderr
//	exit n           exits n
//	say text         prints text, then waits to be killed
//	spawn file       starts `child sleep`, writes its pid to file, waits
//	spawn-exit file  starts `child sleep`, writes its pid to file, exits 0
//	sleep            waits to be killed
func child(args []string) int {
	if len(args) == 0 {
		return 2
	}
	verb, rest := args[0], args[1:]
	switch verb {
	case "argv":
		b, _ := json.Marshal(rest)
		fmt.Println(string(b))
	case "env":
		for _, k := range rest {
			v, ok := os.LookupEnv(k)
			if !ok {
				v = "(unset)"
			}
			fmt.Printf("%s=%s\n", k, v)
		}
	case "env-value":
		fmt.Println(os.Getenv(rest[0]))
	case "cwd":
		dir, _ := os.Getwd()
		fmt.Println(dir)
	case "cat":
		_, _ = io.Copy(os.Stdout, os.Stdin)
	case "stderr":
		fmt.Fprint(os.Stderr, strings.Join(rest, " "))
	case "exit":
		n, _ := strconv.Atoi(rest[0])
		return n
	default:
		return waiting(verb, rest)
	}
	return 0
}

// waiting runs the verbs that start a process or wait.
func waiting(verb string, rest []string) int {
	switch verb {
	case "say":
		fmt.Println(strings.Join(rest, " "))
	case "spawn", "spawn-exit":
		gc := exec.Command(os.Args[0], "child", "sleep")
		if err := gc.Start(); err != nil {
			return 1
		}
		_ = os.WriteFile(rest[0], []byte(strconv.Itoa(gc.Process.Pid)), 0o600)
		if verb == "spawn-exit" {
			return 0
		}
	case "sleep":
	default:
		return 2
	}
	time.Sleep(time.Hour)
	return 0
}

// watched handles this binary run as a copy named watched*: it leaves a
// file beside itself to say it ran, and exits 0 (watchExec on Windows).
func watched() bool {
	exe, err := os.Executable()
	if err != nil || !strings.HasPrefix(filepath.Base(exe), "watched") {
		return false
	}
	_ = os.WriteFile(exe+".ran", nil, 0o600)
	return true
}
