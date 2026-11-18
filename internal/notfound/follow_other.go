//go:build !linux

package notfound

import (
	"context"
	"errors"
)

// Follow needs inotify, which only Linux has; the panel only runs there.
func Follow(ctx context.Context, path string, line func(string), recreate func() error) error {
	return errors.New("404 protection needs Linux")
}
