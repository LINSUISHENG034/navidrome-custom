package mpv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/utils/shellquote"
)

func start(ctx context.Context, args []string, environment ...string) (Executor, error) {
	return startWithInput(ctx, args, nil, nil, environment...)
}

func startWithInput(ctx context.Context, args []string, input io.Reader, errorOutput io.Writer, environment ...string) (Executor, error) {
	if len(args) == 0 {
		return Executor{}, fmt.Errorf("no command arguments provided")
	}
	log.Debug("Executing mpv command", "cmd", args)
	j := Executor{args: args, input: input, errorOutput: errorOutput, environment: environment, done: make(chan struct{})}
	j.PipeReader, j.out = io.Pipe()
	err := j.start(ctx)
	if err != nil {
		return Executor{}, err
	}
	go j.wait()
	return j, nil
}

func (j *Executor) Cancel() error {
	if j.cmd != nil {
		return j.cmd.Cancel()
	}
	return fmt.Errorf("there is non command to cancel")
}

// Stop cancels the subprocess and waits for it to exit or for ctx to expire.
func (j *Executor) Stop(ctx context.Context) error {
	if j.cmd == nil {
		return fmt.Errorf("there is no command to stop")
	}
	if err := j.Cancel(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return j.Wait(ctx)
}

// Wait blocks until the subprocess exits or ctx expires.
func (j *Executor) Wait(ctx context.Context) error {
	if j.done == nil {
		return fmt.Errorf("there is no command to wait for")
	}
	select {
	case <-j.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Executor struct {
	*io.PipeReader
	out         *io.PipeWriter
	args        []string
	input       io.Reader
	errorOutput io.Writer
	environment []string
	cmd         *exec.Cmd
	done        chan struct{}
}

func (j *Executor) start(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, j.args[0], j.args[1:]...) // #nosec
	if len(j.environment) != 0 {
		cmd.Env = append(cmd.Environ(), j.environment...)
	}
	cmd.Stdin = j.input
	cmd.Stdout = j.out
	if j.errorOutput != nil {
		cmd.Stderr = j.errorOutput
	} else if log.IsGreaterOrEqualTo(log.LevelTrace) {
		cmd.Stderr = os.Stderr
	} else {
		cmd.Stderr = io.Discard
	}
	j.cmd = cmd

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting cmd: %w", err)
	}
	return nil
}

func (j *Executor) wait() {
	defer close(j.done)
	if err := j.cmd.Wait(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			_ = j.out.CloseWithError(fmt.Errorf("%s exited with non-zero status code: %d", j.args[0], exitErr.ExitCode()))
		} else {
			_ = j.out.CloseWithError(fmt.Errorf("waiting %s cmd: %w", j.args[0], err))
		}
		return
	}
	_ = j.out.Close()
}

// Path will always be an absolute path
func createMPVCommand(deviceName string, filename string, socketName string) []string {
	// Parse the template structure using shell parsing to handle quoted arguments
	templateArgs, err := shellquote.Split(conf.Server.MPVCmdTemplate)
	if err != nil {
		log.Error("Failed to parse MPV command template", "template", conf.Server.MPVCmdTemplate, err)
		return nil
	}

	// Replace placeholders in each parsed argument to preserve spaces in substituted values
	for i, arg := range templateArgs {
		arg = strings.ReplaceAll(arg, "%d", deviceName)
		arg = strings.ReplaceAll(arg, "%f", filename)
		arg = strings.ReplaceAll(arg, "%s", socketName)
		templateArgs[i] = arg
	}

	// Replace mpv executable references with the configured path
	if len(templateArgs) > 0 {
		cmdPath, err := mpvCommand()
		if err == nil {
			if templateArgs[0] == "mpv" || templateArgs[0] == "mpv.exe" {
				templateArgs[0] = cmdPath
			}
		}
	}

	return templateArgs
}

// This is a 1:1 copy of the stuff in ffmpeg.go, need to be unified.
func mpvCommand() (string, error) {
	mpvOnce.Do(func() {
		if conf.Server.MPVPath != "" {
			mpvPath = conf.Server.MPVPath
			mpvPath, mpvErr = exec.LookPath(mpvPath)
		} else {
			mpvPath, mpvErr = exec.LookPath("mpv")
			if errors.Is(mpvErr, exec.ErrDot) {
				log.Trace("mpv found in current folder '.'")
				mpvPath, mpvErr = exec.LookPath("./mpv")
			}
		}
		if mpvErr == nil {
			log.Info("Found mpv", "path", mpvPath)
			return
		}
	})
	return mpvPath, mpvErr
}

var (
	mpvOnce sync.Once
	mpvPath string
	mpvErr  error
)
