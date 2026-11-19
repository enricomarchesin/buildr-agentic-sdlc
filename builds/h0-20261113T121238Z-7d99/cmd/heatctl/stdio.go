package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// ensureStdio makes sure file descriptors 0, 1 and 2 are open before
// anything else opens a file. If heatctl is started with standard output
// closed, the next socket it opens would otherwise get descriptor 1 and log
// lines would be written into the broker connection. A closed descriptor is
// replaced by /dev/null, or failing that by a pipe whose reader is closed, so
// writes fail harmlessly.
//
// It also turns SIGPIPE into an EPIPE write error: by default a Go program
// writing to a broken stdout pipe is killed, and logging must never affect
// control (L3).
func ensureStdio() {
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
	for fd := 0; fd <= 2; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			continue
		}
		flags := unix.O_WRONLY
		if fd == 0 {
			flags = unix.O_RDONLY
		}
		if nfd, err := unix.Open("/dev/null", flags|unix.O_CLOEXEC, 0); err == nil {
			if nfd != fd {
				_ = unix.Dup2(nfd, fd)
				_ = unix.Close(nfd)
			}
			continue
		}
		var p [2]int
		if err := unix.Pipe(p[:]); err != nil {
			continue
		}
		end := p[1]
		other := p[0]
		if fd == 0 {
			end, other = p[0], p[1]
		}
		if end != fd {
			_ = unix.Dup2(end, fd)
			_ = unix.Close(end)
		}
		if other != fd {
			_ = unix.Close(other)
		}
	}
}
