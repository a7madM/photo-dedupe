// Package exiftime resolves the best-known capture timestamp for an
// image: EXIF DateTimeOriginal when present, filesystem mtime
// otherwise. Per the design spec, the source used is reported
// alongside the timestamp so it can be logged for sanity-checking.
package exiftime

import (
	"os"
	"time"

	"github.com/rwcarlsen/goexif/exif"
)

// Source identifies where a resolved timestamp came from.
type Source string

const (
	SourceEXIF  Source = "exif"
	SourceMtime Source = "mtime"
)

// Resolve returns the best-known capture time for the file at path.
// It tries EXIF DateTimeOriginal first; if the file has no EXIF data
// (or no usable date tag), it falls back to the file's mtime.
func Resolve(path string) (time.Time, Source, error) {
	if t, err := fromEXIF(path); err == nil {
		return t, SourceEXIF, nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, "", err
	}
	return info.ModTime(), SourceMtime, nil
}

func fromEXIF(path string) (time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()

	x, err := exif.Decode(f)
	if err != nil {
		return time.Time{}, err
	}
	return x.DateTime()
}
