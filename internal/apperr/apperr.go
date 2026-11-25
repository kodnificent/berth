// Package apperr carries the stable error codes berth reports and the exit code each
// one maps to. The codes are part of the --json schema; never rename one.
package apperr

import (
	"errors"
	"fmt"
)

type Code string

const (
	Generic          Code = "error"
	Usage            Code = "usage"
	UnknownCommand   Code = "unknown_command"
	UnknownFlag      Code = "unknown_flag"
	NoFreePorts      Code = "no_free_ports"
	WorktreeNotFound Code = "worktree_not_found"
	StateCloneFailed Code = "state_clone_failed"
	ConfigInvalid    Code = "config_invalid"
	MainWorktree     Code = "main_worktree"
	NoBerth          Code = "no_berth"
	// ConfigExists means berth init found the config it would write already there.
	ConfigExists Code = "config_exists"
	// Problems means berth doctor found something wrong.
	Problems Code = "problems_found"
	// ComposeTooOld means the docker compose on this machine would misread the berth's
	// override file, so berth add refused to write one.
	ComposeTooOld Code = "compose_too_old"
)

func (c Code) Exit() int {
	switch c {
	case NoFreePorts:
		return 2
	case WorktreeNotFound:
		return 3
	case StateCloneFailed:
		return 4
	case ConfigInvalid:
		return 5
	default:
		return 1
	}
}

type Error struct {
	Code    Code
	Message string
	// Next holds the steps a user or agent can take about the error, one hint each.
	Next []string
}

func (e *Error) Error() string { return e.Message }

func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func (e *Error) WithNext(next ...string) *Error {
	e.Next = append(e.Next, next...)
	return e
}

// Reported is an error a command has already written out in full, with --json too, so
// only its exit code is left to give.
type Reported struct{ Code Code }

func (r *Reported) Error() string { return string(r.Code) }

func NextOf(err error) []string {
	var e *Error
	if errors.As(err, &e) {
		return e.Next
	}
	return nil
}

func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return Generic
}
