// relay emulates one hop of the judge's `docker run -i` stream chain: it
// copies stdin to a child and the child's stdout back with 32 KiB buffers,
// like the io.Copy loops in the docker CLI, dockerd and containerd-shim.
// usage: relay DEPTH CMD ARGS...   (DEPTH hops are chained)
package main

import (
	"io"
	"os"
	"os/exec"
	"strconv"
)

func main() {
	depth, _ := strconv.Atoi(os.Args[1])
	var cmd *exec.Cmd
	if depth > 1 {
		cmd = exec.Command(os.Args[0], append([]string{strconv.Itoa(depth - 1)}, os.Args[2:]...)...)
	} else {
		cmd = exec.Command(os.Args[2], os.Args[3:]...)
	}
	cmd.Stderr = os.Stderr
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(2)
	}
	go func() {
		io.CopyBuffer(in, os.Stdin, make([]byte, 32<<10))
		in.Close()
	}()
	io.CopyBuffer(os.Stdout, out, make([]byte, 32<<10))
	if err := cmd.Wait(); err != nil {
		os.Exit(1)
	}
}
