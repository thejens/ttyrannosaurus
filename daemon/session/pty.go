package session

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// spawnPTY starts command in a new PTY and returns the PTY file and Cmd.
// dir sets the working directory; if empty it defaults to the user's home directory.
func spawnPTY(command []string, dir string) (*os.File, *exec.Cmd, error) {
	if len(command) == 0 {
		return nil, nil, fmt.Errorf("command must not be empty")
	}
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("start pty: %w", err)
	}
	return ptmx, cmd, nil
}

// resize sets the PTY window size.
func resize(ptmx *os.File, cols, rows uint16) {
	pty.Setsize(ptmx, &pty.Winsize{Cols: cols, Rows: rows}) //nolint:errcheck
}
