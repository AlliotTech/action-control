// Offline replay of the exact parser and MPEG-TS muxer used by the web mirror.
// Usage: go run ./tools/mimo_preview_replay.go -pcap input.pcap -out preview.ts
// No device access. Output must be a new file; damaged input removes that file.
package main

import (
	"action-control/internal/mimopreview"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"
)

func replay() error {
	input := flag.String("pcap", "", "classic Ethernet pcap input")
	output := flag.String("out", "", "new MPEG-TS output file")
	flag.Parse()
	if *input == "" || *output == "" || flag.NArg() != 0 {
		return errors.New("-pcap and -out are required")
	}
	source, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer source.Close()
	dest, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		dest.Close()
		if !complete {
			os.Remove(*output)
		}
	}()
	decoder, mux := &mimopreview.Decoder{}, &mimopreview.Muxer{}
	var size uint64
	err = mimopreview.ReadPCAP(source, func(at time.Time, packet []byte) error {
		unit, err := decoder.Feed(at, packet)
		if err != nil || unit == nil {
			return err
		}
		n, err := dest.Write(mux.Write(unit))
		size += uint64(n)
		return err
	})
	if err != nil {
		return err
	}
	if decoder.Stats.OutputFrames == 0 {
		return errors.New("no playable Mimo video")
	}
	if err := dest.Close(); err != nil {
		return err
	}
	complete = true
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"stats": decoder.Stats, "bytes": size, "clock_corrections": mux.ClockCorrections,
	})
}

func main() {
	if err := replay(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
